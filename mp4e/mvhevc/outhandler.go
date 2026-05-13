package mvhevc

import (
	"github.com/eluv-io/avpipe/goavpipe"
)

// WrapOutputHandler returns a goavpipe.OutputHandler that intercepts Write
// to inject MV-HEVC sample-group and track-group boxes into the moov of the
// first segment passing through, then copies everything else unchanged.
// Seek, Close, and Stat are delegated to the underlay.
func WrapOutputHandler(inner goavpipe.OutputHandler) goavpipe.OutputHandler {
	return &mvhevcOutputHandler{
		inner:   inner,
		patcher: NewStreamPatcher(writerAdapter{inner: inner}),
	}
}

type mvhevcOutputHandler struct {
	inner   goavpipe.OutputHandler
	patcher *StreamPatcher
}

func (h *mvhevcOutputHandler) Write(buf []byte) (int, error) {
	return h.patcher.Write(buf)
}

func (h *mvhevcOutputHandler) Seek(offset int64, whence int) (int64, error) {
	// Flush as a precaution when the muxer peforms a seek
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
// (used by the StreamPatcher)
type writerAdapter struct {
	inner goavpipe.OutputHandler
}

func (a writerAdapter) Write(buf []byte) (int, error) {
	return a.inner.Write(buf)
}
