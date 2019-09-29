package live

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
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

	"eluvio/errors"
	elog "eluvio/log"
)

var log = elog.Get("/eluvio/avpipe/live")

// TESTSaveToDir save manifests and segments to this path if not empty string
var TESTSaveToDir string

// HLSReader provides a reader interface to a live HLS stream - it reads a
//   specified number of segments on outputs one aggregate MPEG-TS buffer
type HLSReader struct {
	sequence          int
	client            http.Client
	url               *url.URL
	numSegmentsNeeded int
	numSegmentsRead   int
}

// NewHLSReader ...
func NewHLSReader(url *url.URL) *HLSReader {
	lhr := HLSReader{
		sequence: -1,
		client:   http.Client{},
		url:      url,
	}
	return &lhr
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
func resolve(urlStr string, base *url.URL) (*url.URL, error) {
	url, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}

	if url.IsAbs() {
		return url, nil
	}

	return base.ResolveReference(url), nil
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
	defer file.Close()

	var content io.ReadCloser
	if content, err = openURL(client, u); err != nil {
		return
	}
	defer content.Close()

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
	defer file.Close()
	return readSegment(client, u, s, file)
}

func readSegment(
	client http.Client, u *url.URL, s *m3u8.MediaSegment, w io.Writer) (
	written int64, err error) {

	log.Debug("AVLR readSegment start", "segment", s)

	msURL, err := resolve(s.URI, u)
	if err != nil {
		log.Error("AVLR Failed to resolve segment URL", "err", err, "segment.URI", s.URI)
		return
	}

	var dw *decryptWriter
	if s.Key != nil {
		var key []byte
		if key, err = downloadKey(u, s.Key.URI); err != nil {
			log.Error("AVLR Failed to download AES key", "err", err, "s.Key.URI", s.Key.URI)
			return
		} else if len(key) != 16 { // assuming s.Key.Method is AES-128
			return 0, errors.E("Bad AES key size", "len", len(key), "s.Key.URI", s.Key.URI)
		}

		var iv []byte
		if len(s.Key.IV) > 0 {
			if iv, err = hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(s.Key.IV, "0x"), "0X")); err != nil {
				log.Error("AVLR Failed to decode AES IV", "err", err, "s.Key.IV", s.Key.IV)
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
	defer content.Close()

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

func readMasterPlaylist(client http.Client, u *url.URL) ([]*m3u8.Variant, error) {
	if len(TESTSaveToDir) > 0 {
		err := saveManifestToFile(client, u, TESTSaveToDir)
		if err != nil {
			log.Error("AVLR readMasterPlaylist saveManifestToFile", "err", err)
		}
	}

	content, err := openURL(client, u)
	if err != nil {
		return nil, err
	}
	defer content.Close()

	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		return nil, err
	}

	if listType != m3u8.MASTER {
		return nil, errors.E("AVLR invalid playlist")
	}

	masterPlaylist := playlist.(*m3u8.MasterPlaylist)
	return masterPlaylist.Variants, nil
}

// readPlaylist HTTP GET the HLS media playlist and process segments up to
// durationSec . Start at sequence number (startSeqNo) specified at offset
// startSec. Return the next sequence number and offset, essentially where
// to continue next time. durationReadSec is the length of video processed.
//
// PENDING(PT) - ideally we can control this with something more precise than duration
func (lhr *HLSReader) readPlaylist(u *url.URL, startSeqNo int,
	startSec float64, durationSec float64, w io.Writer) (
	nextSeqNo int, nextStartSec float64, durationReadSec float64, err error) {

	log.Debug("AVLR readPlaylist start", "startSeqNo", startSeqNo, "startSec", startSec, "durationSec", durationSec)

	if len(TESTSaveToDir) > 0 {
		err := saveManifestToFile(lhr.client, u, TESTSaveToDir)
		if err != nil {
			log.Error("AVLR readPlaylist saveManifestToFile", "err", err)
		}
	}

	content, err := openURL(lhr.client, u)
	if content != nil {
		defer content.Close()
	}
	if err != nil {
		return startSeqNo, startSec, 0, err
	}

	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		return startSeqNo, startSec, 0, err
	} else if listType != m3u8.MEDIA {
		return startSeqNo, startSec, 0,
			errors.E("AVLR unexpected playlist type", "listType", listType)
	}

	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)

	startIndex := -1
	durationToEdge := float64(0)
	edgeSeqNo := 0
	for i, segment := range mediaPlaylist.Segments {
		if segment == nil {
			// No more segments
			if startSeqNo == -1 {
				// First segment in the recording
				if edgeSeqNo <= 1 {
					// Make sure startSeqNo is valid (at least 1)
					log.Info("AVLR waiting for live edge move ahead before initializing", "edgeSeqNo", edgeSeqNo)
					return startSeqNo, startSec, 0, nil
				}
				startSeqNo = edgeSeqNo - 1
				startIndex = i - 2
				log.Info("AVLR initializing recording at live edge", "seqNo", edgeSeqNo)
			} else if startIndex == -1 {
				// Segment not found in the playlist
				if startSeqNo < edgeSeqNo {
					startSeqNo = edgeSeqNo
					startIndex = i - 1
					log.Error("AVLR fell too far behind live edge, skipping ahead", "seqNo", edgeSeqNo)
				} else {
					log.Debug("AVLR no new segments available")
					return startSeqNo, startSec, 0, nil
				}
			}
			break
		}
		edgeSeqNo = int(segment.SeqId)
		if startSeqNo == edgeSeqNo {
			startIndex = i
		} else if startSeqNo < edgeSeqNo && startSeqNo != -1 {
			durationToEdge += segment.Duration
		}
	}
	if durationToEdge > 0 {
		log.Warn("AVLR falling behind live edge", "durationToEdge", durationToEdge, "edgeSeqNo", edgeSeqNo)
	}
	if startIndex < 0 {
		log.Warn("AVLR playlist smaller than expected", "startIndex", startIndex, "edgeSeqNo", edgeSeqNo)
		return startSeqNo, startSec, 0, nil
	}

	nextSeqNo = startSeqNo
	for i := startIndex; ; i++ {
		segment := mediaPlaylist.Segments[i]
		if segment == nil {
			break
		}

		// Assert of sorts
		if nextSeqNo != int(segment.SeqId) {
			log.Warn("AVLR nextSeqNo should equal segment.SeqId", "nextSeqNo", nextSeqNo, "segment.SeqId", segment.SeqId)
			nextSeqNo = int(segment.SeqId)
		}

		segRemainingSec := durationSec - durationReadSec
		log.Info("AVLR processing ingest segment", "seqNo", nextSeqNo, "segment.Duration", segment.Duration, "segRemainingSec",
			segRemainingSec, "durationReadSec", durationReadSec, "URI", segment.URI)

		var written int64
		if len(TESTSaveToDir) == 0 {
			written, err = readSegment(lhr.client, u, segment, w)
		} else {
			written, err = saveSegment(lhr.client, u, segment, TESTSaveToDir)
		}
		if err != nil {
			// Transcoded part of a segment - ErrClosedPipe when avpipe closes the pipe
			if !(err == io.EOF || err == io.ErrClosedPipe) || segment.Duration < segRemainingSec {
				log.Error("AVLR failed to read requested duration", "written", written, "err", err,
					"nextSeqNo", nextSeqNo, "nextStartSec", 0, "durationReadSec", durationReadSec)
				return nextSeqNo, 0, durationReadSec, err
			}
			log.Info("AVLR successfully read requested duration, ending with a partial segment", "written", written, "err", err,
				"nextSeqNo", nextSeqNo, "nextStartSec", segRemainingSec, "durationReadSec", durationSec)
			return nextSeqNo, segRemainingSec, durationSec, nil
		}

		durationReadSec += segment.Duration - startSec
		startSec = 0
		nextSeqNo++
		if durationReadSec >= durationSec {
			log.Info("AVLR successfully read requested duration", "nextSeqNo", nextSeqNo, "nextStartSec", 0, "durationReadSec", durationSec)
			if durationReadSec > durationSec {
				log.Warn("AVLR read more than requested duration", "durationSec", durationSec, "durationReadSec", durationReadSec)
			}
			return nextSeqNo, 0, durationSec, nil
		}
	}

	log.Info("AVLR read all available segments", "nextSeqNo", nextSeqNo, "nextStartSec", startSec, "durationReadSec", durationReadSec)
	return nextSeqNo, startSec, durationReadSec, nil
}

// Fill reads HLS input as indicated by parameters startSesquence and numSegments
// and writes it out to the provided io.Writer
// If startSeqNo is -1, it starts with the first sequence it gets
// The sequence number is 0-based (i.e. the first segment has sequence number 0)
func (lhr *HLSReader) Fill(
	startSeqNo int, startSec float64, durationSecRat *big.Rat, w io.Writer) (
	nextSeqNo int, nextStartSec float64, err error) {

	durationSec, _ := durationSecRat.Float64()

	log.Info("AVLR Fill start", "startSeqNo", startSeqNo, "startSec", startSec, "durationSec", durationSec, "playlist", lhr.url)

	variants, err := readMasterPlaylist(lhr.client, lhr.url)
	if err != nil {
		return startSeqNo, startSec, err
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
		return startSeqNo, startSec,
			errors.E("AVLR variant not found in master playlist", "URL", lhr.url)
	}

	msURL, err := resolve(variant.URI, lhr.url)
	if err != nil || msURL == nil {
		return startSeqNo, startSec,
			errors.E("AVLR failed to resolve variant URL", "err", err, "variant.URI", variant.URI, "URL", lhr.url)
	}
	log.Info("AVLR media playlist found", "URL", msURL, "bandwidth", variant.Bandwidth, "resolution", variant.Resolution,
		"average_bandwidth", variant.AverageBandwidth, "Audio", variant.Audio, "Video", variant.Video)

	for {
		var durationReadSec float64
		nextSeqNo, nextStartSec, durationReadSec, err =
			lhr.readPlaylist(msURL, startSeqNo, startSec, durationSec, w)
		if err != nil {
			log.Error("AVLR failed to read playlist", "nextSeqNo", nextSeqNo, "nextStartSec", nextStartSec, "err", err)
			// TODO: Report errors to the user
			return
		}
		// log.Debug("AVLR readPlaylist returned", "nextSeqNo", nextSeqNo, "nextStartSec", nextStartSec, "durationReadSec", durationReadSec, "err", err)

		if durationReadSec >= durationSec {
			log.Info("AVLR Fill done", "nextSeqNo", nextSeqNo, "nextStartSec", nextStartSec, "err", err)
			return
		} else if durationReadSec > 0 {
			startSeqNo = nextSeqNo
			startSec = nextStartSec
			durationSec -= durationReadSec
		} else {
			// Wait for a new segment - typically segments are 2 sec or longer
			// PENDING(PT) HLS spec says to base the wait time on segment duration
			time.Sleep(time.Duration(4000 * time.Millisecond))
		}
	}
}

// TODO: Move to common/utils

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

func downloadKey(u *url.URL, keyURI string) (body []byte, err error) {
	keyURL, err := resolve(keyURI, u)
	if err != nil {
		log.Error("AVLR Failed to key URL", "err", err, "keyURI", keyURI)
		return
	}

	log.Debug("AVLR HTTP GET AES key", "url", keyURL.String())
	resp, err := http.Get(keyURL.String())
	if err != nil {
		return
	}
	defer resp.Body.Close()
	return ioutil.ReadAll(resp.Body)
}
