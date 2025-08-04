package avpipe

// #cgo pkg-config: libavcodec
// #cgo pkg-config: libavfilter
// #cgo pkg-config: libavformat
// #cgo pkg-config: libavutil
// #cgo pkg-config: libswresample
// #cgo pkg-config: libavresample
// #cgo pkg-config: libavdevice
// #cgo pkg-config: libswscale
// #cgo pkg-config: libavutil
// #cgo pkg-config: libpostproc
// #cgo netint pkg-config: xcoder
// #cgo pkg-config: srt
// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo CFLAGS: -I${SRCDIR}/libavpipe/include
// #cgo CFLAGS: -I${SRCDIR}/utils/include
// #cgo LDFLAGS: -L${SRCDIR}
// #cgo linux LDFLAGS: -Wl,-rpath,$ORIGIN/../lib
// #include "avpipe.h"
import "C"

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"

	smpte "github.com/eluv-io/avpipe/smpte20xx/transport"
)

var outputStillOpenErr = errors.New("cannot open new sequential output, previous still open")
var outputClosedErr = errors.New("output is closed, cannot write to it")
var alreadyClosedErr = errors.New("output already closed")

func NewAVPipeSequentialOutWriter(inFd int64, streamIndex int, streamType goavpipe.AVType, firstSegIdx int) smpte.SequentialOpener {
	return &avpipeSequentialOutHandler{
		inFd:         inFd, // SSDBG not available at construction
		streamIndex:  streamIndex,
		streamType:   streamType,
		nextSegIndex: firstSegIdx,
	}
}

type avpipeSequentialOutHandler struct {
	mu sync.Mutex

	//inFd is the identifier assigned to the input when it was opened. In some places this is called
	//'handler'.
	inFd        int64
	streamIndex int
	streamType  goavpipe.AVType

	nextSegIndex int
	// outFd is the identifier assigned to the output. If nil, it means there are no open outputs.
	outFd *int64
}

func (h *avpipeSequentialOutHandler) OpenNext(inFd int64) (io.WriteCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.inFd = inFd // SSDBG to clean up

	if h.outFd != nil {
		return nil, outputStillOpenErr
	}

	outFd := AVPipeOpenOutputGo(h.inFd, h.streamIndex, h.nextSegIndex, 0, h.streamType)
	if outFd < 0 {
		return nil, fmt.Errorf("failed to open next output for input fd %d", h.inFd)
	}
	h.outFd = &outFd

	// Increment the segment index for the next output.
	h.nextSegIndex++

	return h, nil
}

func (h *avpipeSequentialOutHandler) Write(p []byte) (n int, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.outFd == nil {
		return 0, outputClosedErr
	}

	n = AVPipeWriteOutputGo(h.inFd, *h.outFd, p)
	if n < 0 {
		return 0, fmt.Errorf("failed to write to output fd %d", *h.outFd)
	}

	return n, nil
}

func (h *avpipeSequentialOutHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.outFd == nil {
		return outputClosedErr
	}

	rv := AVPipeCloseOutputGo(h.inFd, *h.outFd)
	if rv < 0 {
		return fmt.Errorf("failed to close output fd %d", *h.outFd)
	}

	h.outFd = nil
	return nil
}
