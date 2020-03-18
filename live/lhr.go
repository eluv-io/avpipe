package live

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/grafov/m3u8"

	"github.com/qluvio/content-fabric/errors"
	elog "github.com/qluvio/content-fabric/log"
	eioutil "github.com/qluvio/content-fabric/util/ioutil"
)

var log = elog.Get("/eluvio/avpipe/live")

type StreamType int

const (
	STUnknown StreamType = iota
	STMuxed
	STAudioOnly
	STVideoOnly
)

// HLSReader provides a reader interface to an HLS playlist that serves a
// live MPEG-TS stream. Close the Pipe to clean up.
//
// An HLS playlist may have zero or more audio and video streams. We choose the
// highest bitrate stream of each type to record. If the master playlist
// advertises a muxed stream with both audio and video, choose the muxed
// stream with the highest bitrate.
type HLSReader struct {
	Pipe            io.ReadWriteCloser //
	Type            StreamType         //
	client          *http.Client       //
	durationReadSec float64            //
	nextSeqNo       int                // The next segment sequence number to record (the first sequence number in a stream is 0)
	playlistPollSec float64            // How often to poll for the manifest - HLS spec recommends half the advertised duration
	playlistURL     *url.URL           //
}

// TESTSaveToDir save manifests and segments to this path if not empty string
var TESTSaveToDir string

type compareVariant = func(a *m3u8.Variant, b *m3u8.Variant) *m3u8.Variant

func audioAlternative(video *m3u8.Variant) (alt *m3u8.Alternative) {
	for _, a := range video.Alternatives {
		if strings.ToLower(a.Type) == "audio" &&
			strings.ToLower(a.GroupId) == strings.ToLower(video.Audio) &&
			(a.Default || alt == nil) {
			alt = a
		}
	}
	return
}

// assumption: a and b are not muxed
func compareAudioVariant(a *m3u8.Variant, b *m3u8.Variant) *m3u8.Variant {
	if isAudioOnly(a) && !isAudioOnly(b) {
		return a
	} else if isAudioOnly(b) && !isAudioOnly(a) {
		return b
	} else if !isAudioOnly(a) && !isAudioOnly(b) {
		return nil
	}
	if a.Bandwidth > b.Bandwidth {
		return a
	} else {
		return b
	}
}

func compareMuxedVariant(a *m3u8.Variant, b *m3u8.Variant) *m3u8.Variant {
	if isMuxed(a) && !isMuxed(b) {
		return a
	} else if isMuxed(b) && !isMuxed(a) {
		return b
	} else if !isMuxed(a) && !isMuxed(b) {
		return nil
	}
	if a.Bandwidth > b.Bandwidth {
		return a
	} else {
		return b
	}
}

// assumption: a and b are not muxed
func compareVideoVariant(a *m3u8.Variant, b *m3u8.Variant) *m3u8.Variant {
	if isVideoOnly(a) && !isVideoOnly(b) {
		return a
	} else if isVideoOnly(b) && !isVideoOnly(a) {
		return b
	} else if !isVideoOnly(a) && !isVideoOnly(b) {
		return nil
	}
	if a.Bandwidth > b.Bandwidth {
		return a
	} else {
		return b
	}
}

func findTopVariant(variants []*m3u8.Variant, compare compareVariant) (
	top *m3u8.Variant) {

	for _, v := range variants {
		top = compare(top, v)
	}
	return
}

func hasVideo(v *m3u8.Variant) bool {
	return v != nil && len(v.Resolution) > 0
}

func isAudioOnly(v *m3u8.Variant) bool {
	return v != nil && !hasVideo(v) && !v.Iframe
}

func isMuxed(v *m3u8.Variant) bool {
	return v != nil && hasVideo(v) && len(v.Audio) == 0 && !v.Iframe
}

func isVideoOnly(v *m3u8.Variant) bool {
	return v != nil && hasVideo(v) && !v.Iframe
}

// Determines readers based on the desired stream type and the playlistURL,
// which can be either a master playlist or media playlist.
//
// Read master playlists only once (section 6.3.4 of the spec only says
// to reload *media* playlists. Also, some servers will return a new
// session token with each HTTP request for the master, but the same token
// must be used to maintain playback state.
//
// TODO Probably should change approach to selecting a variant first, then finding the audio/video streams. Also test against different playlists.
func NewHLSReaders(playlistURL *url.URL, desired StreamType) (
	readers []*HLSReader, err error) {

	logContext := fmt.Sprintf("url=%s", playlistURL.String())
	et := errors.Template("NewHLSReaders", "url", playlistURL.String())
	log.Debug("checking HLS playlist", "c", logContext)

	if len(TESTSaveToDir) > 0 {
		if e := saveManifestToFile(http.DefaultClient, playlistURL, TESTSaveToDir); e != nil {
			log.Error("saveManifestToFile", "err", e)
		}
	}

	var content io.ReadCloser
	if content, err = openURL(http.DefaultClient, playlistURL); err != nil {
		return nil, et(err)
	}
	defer eioutil.CloseCloser(content, log)

	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		return nil, et(err)
	}

	var lhr *HLSReader
	if listType == m3u8.MEDIA {
		if lhr = NewHLSReader(playlistURL, desired); err == nil {
			readers = append(readers, lhr)
		} else {
			err = et(err)
		}
		return
	}

	// From the master playlist, choose the variant with the highest bandwidth
	master := playlist.(*m3u8.MasterPlaylist)

	if v := findTopVariant(master.Variants, compareMuxedVariant); v != nil {
		if lhr, err = NewHLSReaderV(v, playlistURL, STMuxed); err == nil {
			readers = append(readers, lhr)
		} else {
			err = et(err)
		}
		return
	}

	var topVideo *m3u8.Variant
	if desired != STAudioOnly {
		if topVideo = findTopVariant(master.Variants, compareVideoVariant); topVideo != nil {
			if lhr, err = NewHLSReaderV(topVideo, playlistURL, STVideoOnly); err != nil {
				return nil, et(err)
			}
			readers = append(readers, lhr)
		}
	}

	if desired != STVideoOnly {
		var lhr *HLSReader

		// Use audio stream associated with the variant
		if topVideo != nil {
			if alt := audioAlternative(topVideo); alt != nil {
				lhr, err = NewHLSReaderA(alt, playlistURL)
			}
		}

		if lhr == nil {
			if v := findTopVariant(master.Variants, compareAudioVariant); v != nil {
				lhr, err = NewHLSReaderV(v, playlistURL, STAudioOnly)
			}
		}

		if lhr == nil {
			// Hack for grafov bug (not populating VariantParams.Alternatives)
			// TODO: Revisit if/when grafov fixes it
			for _, v := range master.Variants {
				if alt := audioAlternative(v); alt != nil {
					lhr, err = NewHLSReaderA(alt, playlistURL)
					break
				}
			}
		}

		if err != nil {
			if len(readers) > 0 {
				eioutil.CloseCloser(readers[0].Pipe, log)
			}
			return nil, et(err)
		}

		if lhr != nil {
			readers = append(readers, lhr)
		}
	}

	if len(readers) == 0 {
		err = errors.E("parse master playlist", errors.K.Invalid,
			"reason", "failed to find valid variant stream",
			"MasterPlaylist", master)
	}
	return
}

// NewHLSReader creates and returns a media playlist reader, and starts
// goroutines to download the segments. Close the Reader to clean up.
func NewHLSReader(playlistURL *url.URL, t StreamType) *HLSReader {
	return &HLSReader{
		client:          &http.Client{},
		nextSeqNo:       -1,
		playlistPollSec: 5,
		playlistURL:     playlistURL,
		Pipe:            NewRWBuffer(10000),
		Type:            t,
	}
}

func NewHLSReaderV(v *m3u8.Variant, masterPlaylistURL *url.URL, t StreamType) (
	lhr *HLSReader, err error) {

	var playlistURL *url.URL
	if playlistURL, err = resolve(v.URI, masterPlaylistURL); err != nil {
		return
	}

	log.Debug("reading media playlist", "URL", playlistURL,
		"CODECS", v.Codecs,
		"AUDIO", v.Audio,
		"RESOLUTION", v.Resolution,
		"BANDWIDTH", v.Bandwidth,
		"AVERAGE-BANDWIDTH", v.AverageBandwidth,
		"FRAME-RATE", v.FrameRate)

	lhr = NewHLSReader(playlistURL, t)
	return
}

func NewHLSReaderA(a *m3u8.Alternative, masterPlaylistURL *url.URL) (
	lhr *HLSReader, err error) {

	var playlistURL *url.URL
	if playlistURL, err = resolve(a.URI, masterPlaylistURL); err != nil {
		return
	}

	log.Debug("reading audio playlist", "URL", playlistURL,
		"GROUP-ID", a.GroupId,
		"LANGUAGE", a.Language,
		"NAME", a.Name)

	lhr = NewHLSReader(playlistURL, STAudioOnly)
	return
}

func openURL(client *http.Client, u *url.URL) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	} else if resp.StatusCode != 200 {
		return nil, errors.E("AVLR HTTP GET failed", "status", resp.StatusCode, "URL", u.String())
	}

	return resp.Body, nil
}

func (lhr *HLSReader) Start(endChan chan<- error) {
	go func() {
		err := lhr.fill()
		lhr.Pipe.(*RWBuffer).CloseSide(RWBufferWriteClosed)
		endChan <- err
	}()
}

// resolve returns an absolute URL
func resolve(urlStr string, base *url.URL) (u *url.URL, err error) {
	if u, err = url.Parse(urlStr); err != nil || u.IsAbs() {
		return
	}
	return base.ResolveReference(u), nil
}

func saveToFile(client *http.Client, u *url.URL, savePath string) (err error) {
	log.Info("AVLR Saving file to", "path", savePath)
	if err = os.MkdirAll(path.Dir(savePath), 0755); err != nil {
		return
	}
	var file *os.File
	if file, err = os.Create(savePath); err != nil {
		return
	}
	defer eioutil.CloseCloser(file, log)

	var content io.ReadCloser
	if content, err = openURL(client, u); err != nil {
		return
	}
	defer eioutil.CloseCloser(content, log)

	written, err := io.Copy(file, content)
	if err != nil {
		return
	}
	log.Info("AVLR Saved file", "path", savePath, "written", written)

	return nil
}

func saveManifestToFile(client *http.Client, u *url.URL, parentPath string) (
	err error) {

	savePath := path.Join(parentPath, "manifest", u.Path)
	saveDir := path.Dir(savePath)
	saveFile := path.Base(savePath)
	// Prepend timestamp to save snapshots of the changing live manifest
	saveFile = strings.Join([]string{strconv.FormatInt(time.Now().Unix(), 10), saveFile}, "-")
	savePath = path.Join(saveDir, saveFile)
	return saveToFile(client, u, savePath)
}

func saveSegment(
	client *http.Client, u *url.URL, s *m3u8.MediaSegment, parentPath string) (
	written int64, err error) {

	msURL, err := resolve(s.URI, u)
	if err != nil {
		return
	}
	savePath := path.Join(parentPath, msURL.Path)
	log.Info("AVLR Saving segment to", "path", savePath)
	if err = os.MkdirAll(path.Dir(savePath), 0755); err != nil {
		return
	}
	var file *os.File
	if file, err = os.Create(savePath); err != nil {
		return
	}
	defer eioutil.CloseCloser(file, log)
	return readSegment(client, u, s, file)
}

func readSegment(
	client *http.Client, u *url.URL, s *m3u8.MediaSegment, w io.Writer) (
	written int64, err error) {

	log.Debug("AVLR readSegment start", "segment", fmt.Sprintf("%+v", *s))

	msURL, err := resolve(s.URI, u)
	if err != nil {
		log.Error("AVLR Failed to resolve segment URL", "err", err, "uri", s.URI)
		return
	}

	// Handle AES-128 encryption
	// Key should only be set if it changed from the last segment
	var dw *decryptWriter
	if s.Key != nil {
		var key []byte
		if key, err = httpGetBytes(u, s.Key.URI); err != nil {
			log.Error("AVLR Failed to download AES key", "err", err, "uri", s.Key.URI)
			return
		} else if len(key) != 16 { // Assumption: s.Key.Method is AES-128
			return 0, errors.E("Bad AES key size", "len", len(key), "uri", s.Key.URI)
		}

		var iv []byte
		if len(s.Key.IV) > 0 {
			if iv, err = hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(s.Key.IV, "0x"), "0X")); err != nil {
				log.Error("AVLR Failed to decode AES IV", "err", err, "iv", s.Key.IV)
				return
			}
		}

		if dw, err = newDecryptWriter(w, key, iv); err != nil {
			return
		}
	}

	t := time.Now()
	var content io.ReadCloser
	if content, err = openURL(client, msURL); err != nil {
		return
	}
	defer eioutil.CloseCloser(content, log)

	if s.Key != nil {
		if written, err = io.Copy(dw, content); err != nil {
			return
		}
		var n int
		n, err = dw.Flush()
		written += int64(n)
	} else {
		written, err = io.Copy(w, content)
	}
	log.Debug("AVLR readSegment end", "written", written, "err", err, "timeSpent", time.Since(t))
	return
}

// readPlaylist retrieves the media playlist and reads the available segments.
// Starts reading at sequence number lhr.nextSeqNo. Returns complete or error
// when the stream is done or failed irrecoverably
func (lhr *HLSReader) readPlaylist() (
	complete bool, err error) {

	logContext := fmt.Sprintf("url=%s seqNo=%d type=%d",
		lhr.playlistURL.String(), lhr.nextSeqNo, lhr.Type)
	log.Debug("reading media playlist", "c", logContext)
	e := errors.Template("lhr.readPlaylist", "url", lhr.playlistURL.String(),
		"seqNo", lhr.nextSeqNo, "type", lhr.Type)

	if len(TESTSaveToDir) > 0 {
		if err = saveManifestToFile(lhr.client, lhr.playlistURL, TESTSaveToDir); err != nil {
			log.Error("saveManifestToFile", "err", e(err))
		}
	}

	// HTTP GET playlist
	content, err := openURL(lhr.client, lhr.playlistURL)
	defer eioutil.CloseCloser(content, log)
	if err != nil {
		log.Debug("failed to get playlist", "err", err, "c", logContext)
		return // url.Error
	}

	// Decode/Unmarshal
	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		err = e(err)
		return
	} else if listType != m3u8.MEDIA {
		err = errors.E("parse playlist", errors.K.Invalid, e(err),
			"reason", "expected media playlist", "ListType", listType)
		return
	}
	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)

	// 4.3.3.4. EXT-X-ENDLIST indicates that no more Media Segments will be added
	complete = mediaPlaylist.Closed
	if complete {
		log.Info("HLS stream ended", "c", logContext)
	}
	// 6.3.4. When a Playlist file has changed, the client MUST wait for at
	// least the target duration before reload. If it has not changed, wait for
	// one-half the target duration
	if mediaPlaylist.TargetDuration >= 2 {
		lhr.playlistPollSec = mediaPlaylist.TargetDuration / 2
	}

	// Look for the index of the segment to start reading from
	startIndex := -1
	durationToEdge := float64(0)
	edgeSeqNo := 0
	for i, segment := range mediaPlaylist.Segments {
		if segment == nil { // No more segments
			if lhr.nextSeqNo == -1 { // Beginning of recording
				if edgeSeqNo <= 1 { // Ensure startIndex >= 0
					log.Info("waiting for live edge to move ahead before initializing",
						"edgeSeqNo", edgeSeqNo, "c", logContext)
					return
				}
				lhr.nextSeqNo = edgeSeqNo - 1
				startIndex = i - 2
				log.Info("initializing recording at live edge", "nextSeqNo", lhr.nextSeqNo, "c", logContext)
			} else if startIndex == -1 { // Segment not found in the playlist
				if lhr.nextSeqNo < edgeSeqNo {
					lhr.nextSeqNo = edgeSeqNo
					startIndex = i - 1
					log.Error("fell too far behind live edge, skipping ahead",
						"seqNo", edgeSeqNo, "c", logContext)
				} else {
					log.Debug("no new segments available", "c", logContext)
					return
				}
			}
			break
		}
		edgeSeqNo = int(segment.SeqId)
		if lhr.nextSeqNo == edgeSeqNo {
			startIndex = i
		} else if lhr.nextSeqNo < edgeSeqNo && lhr.nextSeqNo != -1 {
			durationToEdge += segment.Duration
		}
	}
	if durationToEdge > mediaPlaylist.TargetDuration {
		log.Warn("falling behind live edge", "durationToEdge", durationToEdge,
			"edgeSeqNo", edgeSeqNo, "c", logContext)
	}
	if startIndex < 0 {
		log.Warn("empty media playlist", "c", logContext)
		return
	}

	// Read segments. Complete when avpipe signals with io.ErrClosedPipe.
	for i := startIndex; ; i++ {
		segment := mediaPlaylist.Segments[i]
		if segment == nil {
			break
		}

		// Sanity check
		if lhr.nextSeqNo != int(segment.SeqId) {
			log.Warn("nextSeqNo should equal segment.SeqId",
				"segment.SeqId", segment.SeqId, "c", logContext)
			lhr.nextSeqNo = int(segment.SeqId)
		}

		log.Debug("processing ingest segment", "URI", segment.URI,
			"segment.Duration", segment.Duration, "c", logContext)
		lhr.durationReadSec += segment.Duration
		var written int64
		if len(TESTSaveToDir) == 0 {
			written, err = readSegment(lhr.client, lhr.playlistURL, segment, lhr.Pipe)
		} else {
			written, err = saveSegment(lhr.client, lhr.playlistURL, segment, TESTSaveToDir)
		}
		if err != nil {
			if err != io.ErrClosedPipe {
				log.Error("error reading HLS segment", "written", written,
					"err", e(err), "durationReadSec", lhr.durationReadSec)
				return
			} else {
				log.Debug("done reading media playlist (transcoding stopped)",
					"written", written, "c", logContext)
				return true, nil
			}
		}
		lhr.nextSeqNo++
	}

	log.Debug("read all available segments",
		"durationReadSec", lhr.durationReadSec, "c", logContext)
	return
}

// fill periodically retrieves the media playlist and reads segments
func (lhr *HLSReader) fill() (err error) {
	logContext := fmt.Sprintf("url=%s type=%d",
		lhr.playlistURL.String(), lhr.Type)
	log.Debug("fill start", "c", logContext)

	lastSeqNo := -1
	lastPlaylistChangeTime := time.Now()
	for {
		var complete bool
		complete, err = lhr.readPlaylist()
		pollingPeriod := time.Duration(lhr.playlistPollSec * float64(time.Second))
		if complete {
			break
		} else if err != nil {
			if _, ok := err.(*url.Error); ok {
				// don't break - retry for HTTP error
			} else if err == io.ErrClosedPipe {
				// the pipe reader was closed
				err = nil
				break
			} else {
				break
			}
		}
		if lastSeqNo != -1 && lastSeqNo == lhr.nextSeqNo &&
			time.Since(lastPlaylistChangeTime) > pollingPeriod*6 {
			// Wait 3x target duration
			log.Info("recording stopped - server stopped publishing",
				"timeout", lhr.playlistPollSec*6, "c", logContext)
			break
		}
		if lastSeqNo != lhr.nextSeqNo {
			lastPlaylistChangeTime = time.Now()
			lastSeqNo = lhr.nextSeqNo
		}
		time.Sleep(pollingPeriod)
	}

	log.Debug("fill done", "err", err, "c", logContext)
	return
}

// TODO: Move code below to common/utils

func unpadPKCS5(src []byte) []byte {
	srclen := len(src)
	padlen := int(src[srclen-1])
	return src[:(srclen - padlen)]
}

type decryptWriter struct {
	cipher    cipher.BlockMode
	writer    io.Writer
	remainder []byte
}

// Write writes len(p) bytes from p to the underlying data stream.
// It returns the number of bytes written from p (0 <= n <= len(p))
// and any error encountered that caused the write to stop early.
// Write must return a non-nil error if it returns n < len(p).
// Write must not modify the slice data, even temporarily.
//
// Implementations must not retain p.
func (dw *decryptWriter) Write(p []byte) (n int, err error) {
	src := append(dw.remainder, p...)

	// Decrypt only multiples of the AES block size. Always hold onto a block
	// (1 to 16 bytes) to remove padding later.
	writeLen := len(src)
	remainder := writeLen % dw.cipher.BlockSize()
	if remainder == 0 {
		remainder = dw.cipher.BlockSize()
	}
	writeLen = writeLen - remainder
	dw.cipher.CryptBlocks(src, src[:writeLen])
	dw.remainder = src[writeLen:]
	n, err = dw.writer.Write(src[:writeLen])
	return len(p), err // lie about len written to satisfy io.Writer interface
}

// Flush MUST be called at the end to take care of un-padding
func (dw *decryptWriter) Flush() (n int, err error) {
	if len(dw.remainder) != dw.cipher.BlockSize() {
		return 0, errors.E("Expected a 16 byte block remainder", len(dw.remainder))
	}
	dw.cipher.CryptBlocks(dw.remainder, dw.remainder)
	return dw.writer.Write(unpadPKCS5(dw.remainder))
}

func newDecryptWriter(writer io.Writer, key []byte, iv []byte) (*decryptWriter, error) {
	dw := &decryptWriter{
		cipher: nil,
		writer: writer,
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return dw, err
	}
	dw.cipher = cipher.NewCBCDecrypter(block, iv)

	return dw, nil
}

func httpGetBytes(base *url.URL, uri string) (body []byte, err error) {
	u, err := resolve(uri, base)
	if err != nil {
		return
	}

	log.Debug("HTTP GET", "url", u.String())
	resp, err := http.Get(u.String())
	if err != nil {
		return
	}
	defer eioutil.CloseCloser(resp.Body, log)
	return ioutil.ReadAll(resp.Body)
}
