package avpipe

// #include "avpipe.h"

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/eluv-io/avpipe/broadcastproto/mpegts"
	"github.com/eluv-io/avpipe/goavpipe"
)

var errOutputStillOpen = errors.New("cannot open new sequential output, previous still open")
var errOutputClosed = errors.New("output is closed, cannot write to it")
var errAlreadyClosed = errors.New("output already closed")

func NewAVPipeSequentialOutWriter(inFd int64, streamIndex int, streamType goavpipe.AVType) mpegts.SequentialOpener {
	return &avpipeSequentialOutHandler{
		inFd:         inFd,
		streamIndex:  streamIndex,
		streamType:   streamType,
		nextSegIndex: 1,
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

func (h *avpipeSequentialOutHandler) OpenNext() (io.WriteCloser, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.outFd != nil {
		return nil, errOutputStillOpen
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
		return 0, errOutputClosed
	}

	s := time.Now()
	n = AVPipeWriteOutputGo(h.inFd, *h.outFd, p)
	dur := time.Since(s)
	if dur > 50*time.Millisecond {
		goavpipe.Log.Warn("AVPipeWriteOutputGo took too long", "duration", dur)
	}
	if n < 0 {
		return 0, fmt.Errorf("failed to write to output fd %d", *h.outFd)
	}

	return n, nil
}

func (h *avpipeSequentialOutHandler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.outFd == nil {
		return errOutputClosed
	}

	rv := AVPipeCloseOutputGo(h.inFd, *h.outFd)
	if rv < 0 {
		return fmt.Errorf("failed to close output fd %d", *h.outFd)
	}

	h.outFd = nil
	return nil
}
