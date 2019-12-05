package live

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"math/big"
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
)

var log = elog.Get("/eluvio/avpipe/live")

// TESTSaveToDir save manifests and segments to this path if not empty string
var TESTSaveToDir string

// HLSReader provides a reader interface to a live HLS stream - it reads a
//   specified number of segments on outputs one aggregate MPEG-TS buffer
type HLSReader struct {
	client          http.Client
	masterURL       *url.URL
	playlistURL     *url.URL
	playlistPollSec float64    // How often to poll for the manifest - HLS spec says half the advertised duration
	nextSeqNo       int        // The next segment sequence number to record (the first sequence number in a stream is 0)
	NextSkipOverPts int64      // Start recording from this frame in the segment
	segments        []*segInfo // Segments recorded
}

type segInfo struct {
	// buffer TODO: keep segment data to avoid downloading again
	duration float64
	seqNo    int
}

// NewHLSReader ...
// Deprecated
func NewHLSReader(masterURL *url.URL) *HLSReader {
	lhr := &HLSReader{
		client:          http.Client{},
		masterURL:       masterURL,
		playlistPollSec: 1,
		nextSeqNo:       -1,
	}
	return lhr
}

// NewHLSReader creates an HLS reader and returns an io.Reader
// Starts the necessary goroutines - when the returned reader is closed, it stops
// all goroutines and cleans up.
// PENDING(SSS) - it has a max duration of 24h
func NewHLSReaderV2(masterURL *url.URL) io.ReadWriteCloser {

	rwb := NewRWBuffer(10000)

	lhr := &HLSReader{
		client:          http.Client{},
		masterURL:       masterURL,
		playlistPollSec: 1,
		nextSeqNo:       -1,
	}

	go func() {
		err := lhr.fill(rwb)
		rwb.(*RWBuffer).CloseSide(RWBufferWriteClosed)
		log.Info("AVLR Fill done", "err", err)
	}()

	return rwb
}

// durationReadSec is the sum of all the ingest segment duration used to create
// this part (video clip)
func (lhr *HLSReader) durationReadSec() (total float64) {
	for _, segment := range lhr.segments {
		total += segment.duration
	}
	return total
}

// prepareNext determines the nextSeqNo to use on the next call to Fill, and
// TODO: cleans up segInfo no longer needed
func (lhr *HLSReader) prepareNext() {
	indexOfSegmentsToKeep := len(lhr.segments)
	var durationOverlap float64
	for i := len(lhr.segments) - 1; i >= 0; i-- {
		segment := lhr.segments[i]
		durationOverlap += segment.duration
		// TODO: We're assuming transcoding keeps up. Check if controlling
		//       ffmpeg input buffer size can ensure this.
		// Reuse at least half the last target duration in the next part
		if durationOverlap > lhr.playlistPollSec {
			lhr.nextSeqNo = segment.seqNo
			indexOfSegmentsToKeep = i
			break
		}
	}
	lhr.segments = lhr.segments[indexOfSegmentsToKeep:]
}

func openURL(client http.Client, u *url.URL) (io.ReadCloser, error) {
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

// resolve returns an absolute URL
func resolve(urlStr string, base *url.URL) (u *url.URL, err error) {
	if u, err = url.Parse(urlStr); err != nil || u.IsAbs() {
		return
	}
	return base.ResolveReference(u), nil
}

func saveToFile(client http.Client, u *url.URL, savePath string) (err error) {
	log.Info("AVLR Saving file to", "path", savePath)
	if err = os.MkdirAll(path.Dir(savePath), 0755); err != nil {
		return
	}
	var file *os.File
	if file, err = os.Create(savePath); err != nil {
		return
	}
	defer closeCloser(file)

	var content io.ReadCloser
	if content, err = openURL(client, u); err != nil {
		return
	}
	defer closeCloser(content)

	written, err := io.Copy(file, content)
	if err != nil {
		return
	}
	log.Info("AVLR Saved file", "path", savePath, "written", written)

	return nil
}

func saveManifestToFile(client http.Client, u *url.URL, parentPath string) (
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
	client http.Client, u *url.URL, s *m3u8.MediaSegment, parentPath string) (
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
	defer closeCloser(file)
	return readSegment(client, u, s, file)
}

func readSegment(
	client http.Client, u *url.URL, s *m3u8.MediaSegment, w io.Writer) (
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
	defer closeCloser(content)

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

// readPlaylist HTTP GET the HLS media playlist and record segments up to
// durationSec. Starts reading at sequence number lhr.nextSeqNo.
func (lhr *HLSReader) readPlaylist(u *url.URL, durationSec float64, w io.Writer) (
	complete bool, err error) {

	log.Debug("AVLR readPlaylist start", "nextSeqNo", lhr.nextSeqNo, "durationSec", durationSec)

	if len(TESTSaveToDir) > 0 {
		if err = saveManifestToFile(lhr.client, u, TESTSaveToDir); err != nil {
			log.Error("AVLR readPlaylist saveManifestToFile", "err", err)
		}
	}

	var content io.ReadCloser
	if content, err = openURL(lhr.client, u); err != nil {
		return
	}
	defer closeCloser(content)

	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		return
	} else if listType != m3u8.MEDIA {
		return false, errors.E("AVLR expected media playlist", "ListType", listType)
	}
	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)
	lhr.playlistPollSec = mediaPlaylist.TargetDuration / 2

	// Look for the index of the segment to start reading from
	startIndex := -1
	durationToEdge := float64(0)
	edgeSeqNo := 0
	for i, segment := range mediaPlaylist.Segments {
		if segment == nil { // No more segments
			if lhr.nextSeqNo == -1 { // Beginning of recording
				if edgeSeqNo <= 1 { // Ensure startIndex >= 0
					log.Info("AVLR waiting for live edge move ahead before initializing", "edgeSeqNo", edgeSeqNo)
					return
				}
				lhr.nextSeqNo = edgeSeqNo - 1
				startIndex = i - 2
				log.Info("AVLR initializing recording at live edge", "seqNo", lhr.nextSeqNo)
			} else if startIndex == -1 { // Segment not found in the playlist
				if lhr.nextSeqNo < edgeSeqNo {
					lhr.nextSeqNo = edgeSeqNo
					startIndex = i - 1
					log.Error("AVLR fell too far behind live edge, skipping ahead", "seqNo", edgeSeqNo)
				} else {
					log.Debug("AVLR no new segments available")
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
		log.Warn("AVLR falling behind live edge", "durationToEdge", durationToEdge, "edgeSeqNo", edgeSeqNo)
	}
	if startIndex < 0 {
		log.Warn("AVLR empty media playlist")
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
			log.Warn("AVLR nextSeqNo should equal segment.SeqId", "nextSeqNo", lhr.nextSeqNo, "segment.SeqId", segment.SeqId)
			lhr.nextSeqNo = int(segment.SeqId)
		}

		// Record segments processed
		s := &segInfo{
			seqNo:    int(segment.SeqId),
			duration: segment.Duration,
		}
		lhr.segments = append(lhr.segments, s)

		log.Debug("AVLR processing ingest segment", "seqNo", lhr.nextSeqNo, "segment.Duration", segment.Duration, "URI", segment.URI)
		var written int64
		if len(TESTSaveToDir) == 0 {
			written, err = readSegment(lhr.client, u, segment, w)
		} else {
			written, err = saveSegment(lhr.client, u, segment, TESTSaveToDir)
		}
		if err != nil {
			if err != io.ErrClosedPipe || lhr.durationReadSec() < durationSec {
				log.Error("AVLR failed to read requested duration", "written", written, "err", err, "durationReadSec", lhr.durationReadSec())
				return
			}
			// Transcoded part of a segment
			log.Debug("AVLR successfully read requested duration, ending with a partial segment", "written", written, "err", err)
			return true, nil
		}
		if lhr.durationReadSec() >= durationSec+(2*mediaPlaylist.TargetDuration) {
			// If this occurs avpipe is either not stopping at the specified
			// duration, or transcoding slower than expected
			log.Info("AVLR successfully read requested duration + 2x source segment duration", "nextSeqNo", lhr.nextSeqNo, "durationReadSec", lhr.durationReadSec())
			return true, nil
		}

		lhr.nextSeqNo++
	}

	log.Debug("AVLR read all available segments", "nextSeqNo", lhr.nextSeqNo, "durationReadSec", lhr.durationReadSec())
	return
}

func readMasterPlaylist(client http.Client, u *url.URL) ([]*m3u8.Variant, error) {
	if len(TESTSaveToDir) > 0 {
		err := saveManifestToFile(client, u, TESTSaveToDir)
		if err != nil {
			log.Error("AVLR readMasterPlaylist saveManifestToFile", "err", err)
		}
	}

	log.Debug("AVLR readMasterPlaylist", "url", u)
	content, err := openURL(client, u)
	if err != nil {
		return nil, err
	}
	defer closeCloser(content)

	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		return nil, err
	} else if listType != m3u8.MASTER {
		return nil, errors.E("AVLR expected master playlist", "ListType", listType)
	}
	masterPlaylist := playlist.(*m3u8.MasterPlaylist)

	return masterPlaylist.Variants, nil
}

// fill records the HLS stream and writes it to the io.Writer passed in as parameter.
// It exits when the io.Writer is closed
func (lhr *HLSReader) fill(w io.Writer) (err error) {

	log.Debug("AVLR Fill start")

	// TODO: Move playlist handling to another function
	if lhr.playlistURL == nil {
		// Read the master playlist only once. Some servers will return a new
		// session token with each HTTP request for the master. We save the variant
		// URL to use the same session. Otherwise video may not continue where
		// left off.
		var variants []*m3u8.Variant
		if variants, err = readMasterPlaylist(lhr.client, lhr.masterURL); err != nil {
			return
		}
		// Choose the variant with the highest bandwidth
		var variant *m3u8.Variant
		for _, v := range variants {
			if variant == nil || v.Bandwidth > variant.Bandwidth {
				if v.FrameRate <= 30 { // PENDING(SSS) Temporary to avoid Fox stream frames with fractional ts duration
					variant = v
				}
			}
		}
		if variant == nil {
			return errors.E("AVLR variant not found in master playlist")
		}

		if lhr.playlistURL, err = resolve(variant.URI, lhr.masterURL); err != nil {
			return errors.E("AVLR failed to resolve variant URL", "err", err, "variant.URI", variant.URI, "URL", lhr.masterURL)
		}
		log.Debug("AVLR media playlist found", "URL", lhr.playlistURL, "bandwidth", variant.Bandwidth, "resolution", variant.Resolution, "average_bandwidth", variant.AverageBandwidth, "Audio", variant.Audio, "Video", variant.Video)
	}

	durationSec := 86400.00 // PENDING(SSS) eliminate need for durationSec

	for {
		var complete bool
		if complete, err = lhr.readPlaylist(lhr.playlistURL, durationSec, w); complete || err != nil {
			// The reader was closed - exit
			if complete || err == io.ErrClosedPipe {
				err = nil
			}
			break
		}
		time.Sleep(time.Duration(lhr.playlistPollSec * float64(time.Second)))
	}

	log.Debug("AVLR Fill done", "err", err)
	return
}

// Fill records the specified duration from the HLS stream to the io.Writer.
// If lhr.nextSeqNo is -1, near the live edge.
// Deprecated
func (lhr *HLSReader) Fill(durationSecRat *big.Rat, w io.Writer) (err error) {
	durationSec, _ := durationSecRat.Float64()

	log.Debug("AVLR Fill start", "durationSec", durationSec)

	// TODO: Move playlist handling to another function
	if lhr.playlistURL == nil {
		// Read the master playlist only once. Some servers will return a new
		// session token with each HTTP request for the master. We save the variant
		// URL to use the same session. Otherwise video may not continue where
		// left off.
		var variants []*m3u8.Variant
		if variants, err = readMasterPlaylist(lhr.client, lhr.masterURL); err != nil {
			return
		}
		// Choose the variant with the highest bandwidth
		var variant *m3u8.Variant
		for _, v := range variants {
			if variant == nil || v.Bandwidth > variant.Bandwidth {
				if v.FrameRate <= 30 { // PENDING(SSS) Temporary to avoid Fox stream frames with fractional ts duration
					variant = v
				}
			}
		}
		if variant == nil {
			return errors.E("AVLR variant not found in master playlist")
		}

		if lhr.playlistURL, err = resolve(variant.URI, lhr.masterURL); err != nil {
			return errors.E("AVLR failed to resolve variant URL", "err", err, "variant.URI", variant.URI, "URL", lhr.masterURL)
		}
		log.Debug("AVLR media playlist found", "URL", lhr.playlistURL, "bandwidth", variant.Bandwidth, "resolution", variant.Resolution, "average_bandwidth", variant.AverageBandwidth, "Audio", variant.Audio, "Video", variant.Video)
	}

	for {
		var complete bool
		if complete, err = lhr.readPlaylist(lhr.playlistURL, durationSec, w); err != nil {
			// TODO retry?
			return
		}
		if complete {
			lhr.prepareNext()
			log.Debug("AVLR Fill done")
			return nil
		}
		time.Sleep(time.Duration(lhr.playlistPollSec * float64(time.Second)))
	}
}

// TODO: Move code below to common/utils

// closeCloser lets us catch close errors when deferred
func closeCloser(c io.Closer) {
	err := c.Close()
	if err != nil {
		log.Error("Close error", "err", err)
	}
}

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

	log.Debug("AVLR HTTP GET", "url", u.String())
	resp, err := http.Get(u.String())
	if err != nil {
		return
	}
	defer closeCloser(resp.Body)
	return ioutil.ReadAll(resp.Body)
}
