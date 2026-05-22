package avpipe

import (
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
	mvhevcpatch "github.com/eluv-io/avpipe/mp4e/mvhevc"
)

//
// The MVHEVC 'restore handler' is in charge of restoring MV-HEVC mp4/bitstream signaling
// that is removed by the ffmpeg bypass mode (both mez and ABR segments).
//
// The handler is only installed when XcParams video layout is MV-HEVC
// It uses the mp4e.StreamPatcher which currently only restores MV-HEVC signaling.
//

var mvhevcRestoreState = struct {
	sync.Mutex
	urlRefs    map[string]int
	handleURLs map[int32]string
}{
	urlRefs:    make(map[string]int),
	handleURLs: make(map[int32]string),
}

func isMvhevcLayout(params *goavpipe.XcParams) bool {
	return params != nil && params.VideoLayout == int32(goavpipe.VideoLayoutMVHEVC)
}

func incrementMvhevcRestoreURL(url string) {
	if url == "" {
		return
	}

	mvhevcRestoreState.Lock()
	mvhevcRestoreState.urlRefs[url]++
	mvhevcRestoreState.Unlock()
}

func registerMvhevcRestoreURL(url string) func() {
	incrementMvhevcRestoreURL(url)

	return func() {
		unregisterMvhevcRestoreURL(url)
	}
}

func registerMvhevcRestoreHandle(handle int32, url string) {
	incrementMvhevcRestoreURL(url)
	mvhevcRestoreState.Lock()
	mvhevcRestoreState.handleURLs[handle] = url
	mvhevcRestoreState.Unlock()
}

func unregisterMvhevcRestoreHandle(handle int32) {
	mvhevcRestoreState.Lock()
	url, ok := mvhevcRestoreState.handleURLs[handle]
	if ok {
		delete(mvhevcRestoreState.handleURLs, handle)
	}
	mvhevcRestoreState.Unlock()
	if ok {
		unregisterMvhevcRestoreURL(url)
	}
}

func unregisterMvhevcRestoreURL(url string) {
	if url == "" {
		return
	}

	mvhevcRestoreState.Lock()
	defer mvhevcRestoreState.Unlock()
	if mvhevcRestoreState.urlRefs[url] <= 1 {
		delete(mvhevcRestoreState.urlRefs, url)
		return
	}
	mvhevcRestoreState.urlRefs[url]--
}

func shouldRestoreMvhevcForURL(url string) bool {
	mvhevcRestoreState.Lock()
	defer mvhevcRestoreState.Unlock()
	return mvhevcRestoreState.urlRefs[url] > 0
}

func isFmp4VideoOutput(t goavpipe.AVType) bool {
	// FMP4VideoSegment: fmp4-segment format (per-segment files, ftyp+moov+moof+mdat)
	// FMP4Stream:       fmp4 format (single fragmented file)
	// DASHVideoInit:    dash format init segment (ftyp+moov, separate file from chunks)
	switch t {
	case goavpipe.FMP4VideoSegment, goavpipe.FMP4Stream, goavpipe.DASHVideoInit:
		return true
	}
	return false
}

func maybeWrapMvhevcOutputHandler(outHandler goavpipe.OutputHandler, restoreMvhevc bool, outType goavpipe.AVType) goavpipe.OutputHandler {
	if restoreMvhevc && isFmp4VideoOutput(outType) {
		return wrapMvhevcOutputHandler(outHandler)
	}
	return outHandler
}

func wrapMvhevcOutputHandler(inner goavpipe.OutputHandler) goavpipe.OutputHandler {
	return &mvhevcOutputHandler{
		inner:   inner,
		patcher: mvhevcpatch.NewStreamPatcher(writerAdapter{inner: inner}),
	}
}

type mvhevcOutputHandler struct {
	inner   goavpipe.OutputHandler
	patcher *mvhevcpatch.StreamPatcher
}

func (h *mvhevcOutputHandler) Write(buf []byte) (int, error) {
	return h.patcher.Write(buf)
}

func (h *mvhevcOutputHandler) Seek(offset int64, whence int) (int64, error) {
	// Flush as a precaution when the muxer performs a seek.
	if err := h.patcher.Flush(); err != nil {
		return 0, err
	}
	return h.inner.Seek(offset, whence)
}

func (h *mvhevcOutputHandler) Close() error {
	if err := h.patcher.Flush(); err != nil {
		return err
	}
	return h.inner.Close()
}

func (h *mvhevcOutputHandler) Stat(streamIndex int, avType goavpipe.AVType, statType goavpipe.AVStatType, statArgs interface{}) error {
	return h.inner.Stat(streamIndex, avType, statType, statArgs)
}

// writerAdapter exposes a goavpipe.OutputHandler's Write as a plain io.Writer
// for the MV-HEVC StreamPatcher.
type writerAdapter struct {
	inner goavpipe.OutputHandler
}

func (a writerAdapter) Write(buf []byte) (int, error) {
	return a.inner.Write(buf)
}
