package avpipe

/*
#include <stdlib.h>
#include "avpipe_xc.h"
#include "avpipe_overlay.h"
#include "avpipe.h"
*/
import "C"

import (
	"fmt"
	"runtime/cgo"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/eluv-io/avpipe/goavpipe"
)

// eventSourceHandle owns the job's EventSource while a transcode runs. The C side holds the cgo handle and calls
// AVPipeEventAt with it once per enabled consumer per decoded frame. cancel closes the source without deleting a
// handle an in-flight call may still use; release deletes it once the job is over.
type eventSourceHandle struct {
	src         goavpipe.EventSource
	url         string
	handle      cgo.Handle
	overlay     *overlayEncoder // nil when the job has no overlay
	cancelled   atomic.Bool
	closeOnce   sync.Once
	closeErr    error
	releaseOnce sync.Once
	asks        atomic.Int64
	events      atomic.Int64
}

// newEventSource wraps params.Events, or returns nil when the job has no consumer of timed events. A consumer
// without a source, or whose kind the source does not answer for, is a parameter error.
func newEventSource(params *goavpipe.XcParams) (*eventSourceHandle, error) {
	if params.Overlay == nil {
		return nil, nil
	}
	if params.Events == nil {
		return nil, fmt.Errorf("overlay: no event source (XcParams.Events)")
	}
	kinds := params.Events.Kinds()
	if !containsString(kinds, goavpipe.KindOverlay) {
		return nil, fmt.Errorf("overlay: the event source answers for %v, not %q", kinds, goavpipe.KindOverlay)
	}
	h := &eventSourceHandle{src: params.Events, url: params.Url, overlay: newOverlayEncoder(params.Overlay)}
	h.handle = cgo.NewHandle(h)
	return h, nil
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (h *eventSourceHandle) close() error {
	h.closeOnce.Do(func() {
		h.closeErr = h.src.Close()
	})
	return h.closeErr
}

func (h *eventSourceHandle) cancel() {
	h.cancelled.Store(true)
	_ = h.close()
}

func (h *eventSourceHandle) release() {
	h.releaseOnce.Do(func() {
		if err := h.close(); err != nil {
			goavpipe.Log.Warn("event source close failed", "err", err, "url", h.url)
		}
		h.handle.Delete()
		goavpipe.Log.Info("event source released", "url", h.url,
			"asks", h.asks.Load(), "events", h.events.Load(), "cancelled", h.cancelled.Load())
	})
}

// initCEventSource points the C job at the Go source.
func initCEventSource(cparams *C.xcparams_t, h *eventSourceHandle) {
	if h != nil {
		C.init_event_source(cparams, C.uintptr_t(h.handle))
	}
}

//export AVPipeEventAt
func AVPipeEventAt(handle C.uintptr_t, cparams *C.xcparams_t, kind *C.char, pts C.int64_t, haveSeq C.int64_t,
	out unsafe.Pointer) C.int {
	return C.int(eventAt(uintptr(handle), cparams, C.GoString(kind), int64(pts), int64(haveSeq), out))
}

// eventAt is AVPipeEventAt with Go types: the consumer of kind asks for the event in effect at pts, holding
// haveSeq. It answers with the C EV_AT_* codes and never blocks. A handle that release() has deleted panics in
// cgo.Handle; that must not reach the C caller.
func eventAt(handle uintptr, cparams *C.xcparams_t, kind string, pts, haveSeq int64, out unsafe.Pointer) (rc int) {
	defer func() {
		if r := recover(); r != nil {
			goavpipe.Log.Error("AVPipeEventAt()", "reason", "source handle no longer valid", "panic", r)
			rc = C.EV_AT_FAILED
		}
	}()

	h, ok := cgo.Handle(handle).Value().(*eventSourceHandle)
	if !ok {
		goavpipe.Log.Error("AVPipeEventAt()", "reason", "not an event source handle")
		return C.EV_AT_FAILED
	}
	if h.cancelled.Load() {
		return C.EV_AT_CANCELLED
	}
	h.asks.Add(1)

	ev, status, err := h.src.At(kind, pts)
	if err != nil {
		if h.cancelled.Load() {
			return C.EV_AT_CANCELLED
		}
		goavpipe.Log.Error("AVPipeEventAt()", err, "reason", "event source failed", "kind", kind, "pts", pts,
			"url", h.url)
		return C.EV_AT_FAILED
	}
	switch status {
	case goavpipe.StatusNotYet:
		return C.EV_AT_NOT_YET
	case goavpipe.StatusEnded:
		return C.EV_AT_ENDED
	}
	if ev == nil || ev.Timing().Seq == haveSeq {
		return C.EV_AT_UNCHANGED
	}

	switch kind {
	case goavpipe.KindOverlay:
		oe, ok := overlayEventOf(ev)
		if !ok || h.overlay == nil {
			goavpipe.Log.Error("AVPipeEventAt()", "reason", "not an overlay event", "type", fmt.Sprintf("%T", ev),
				"url", h.url)
			return C.EV_AT_FAILED
		}
		status, err := h.overlay.encode(h.src, cparams, oe, (*C.ov_event_t)(out))
		if err != nil {
			if h.cancelled.Load() {
				return C.EV_AT_CANCELLED
			}
			goavpipe.Log.Error("AVPipeEventAt()", err, "reason", "bad overlay event", "seq", oe.Seq, "pts", oe.Pts,
				"url", h.url)
			return C.EV_AT_FAILED
		}
		if status == goavpipe.StatusNotYet {
			return C.EV_AT_NOT_YET
		}
		if status == goavpipe.StatusEnded {
			return C.EV_AT_FAILED
		}
	default:
		goavpipe.Log.Error("AVPipeEventAt()", "reason", "no consumer for kind", "kind", kind, "url", h.url)
		return C.EV_AT_FAILED
	}
	h.events.Add(1)
	goavpipe.Log.Debug("AVPipeEventAt()", "kind", kind, "pts", pts, "seq", ev.Timing().Seq, "url", h.url)
	return C.EV_AT_NEW
}

// overlayEventOf accepts an OverlayEvent by value or by pointer.
func overlayEventOf(ev goavpipe.Timed) (*goavpipe.OverlayEvent, bool) {
	switch e := ev.(type) {
	case goavpipe.OverlayEvent:
		return &e, true
	case *goavpipe.OverlayEvent:
		return e, e != nil
	}
	return nil, false
}
