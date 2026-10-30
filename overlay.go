package avpipe

/*
#include <stdlib.h>
#include "avpipe_xc.h"
#include "avpipe_overlay.h"
#include "avpipe.h"
*/
import "C"

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"math"
	"runtime/cgo"
	"sort"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/eluv-io/avpipe/goavpipe"
)

const (
	overlayImageHidden  = -1 // C OV_IMAGE_HIDDEN
	overlayImageDefault = -2 // C OV_IMAGE_DEFAULT
	overlayImageInline  = -3 // the event brings the PNG (overlayRow.png); added to the C library when the event is set
)

var overlaySlotTypes = map[string]C.int{
	goavpipe.OverlaySlotText:  C.int(C.ov_slot_text),
	goavpipe.OverlaySlotClock: C.int(C.ov_slot_clock),
	goavpipe.OverlaySlotImage: C.int(C.ov_slot_image),
}

var overlayAligns = map[string]C.int{
	"":       C.int(C.ov_align_left),
	"left":   C.int(C.ov_align_left),
	"center": C.int(C.ov_align_center),
	"right":  C.int(C.ov_align_right),
}

var overlayClockFormats = map[string]C.int{
	"":        C.int(C.ov_clock_mmss),
	"mm:ss":   C.int(C.ov_clock_mmss),
	"h:mm:ss": C.int(C.ov_clock_hmmss),
}

type overlayClock struct {
	ms      int64
	running bool
	up      bool
}

// overlayRow is one event resolved against the layout: a value per slot index.
type overlayRow struct {
	text  []*string       // nil = no value
	clock []*overlayClock // nil = no value
	image []int           // library index, overlayImageHidden, overlayImageDefault or overlayImageInline
	png   [][]byte        // the PNG of an overlayImageInline slot
}

// overlayInlinePNG recognizes an image slot value that carries the picture itself: PNG bytes, or {"png": base64}
// as the value reads back from JSON. It returns nil for a library name.
func overlayInlinePNG(v any) ([]byte, error) {
	var b []byte
	switch x := v.(type) {
	case []byte:
		b = x
	case map[string]any:
		s, ok := x["png"].(string)
		if !ok || len(x) != 1 {
			return nil, fmt.Errorf("wants {\"png\": base64}")
		}
		var err error
		if b, err = base64.StdEncoding.DecodeString(s); err != nil {
			return nil, fmt.Errorf("png is not base64: %w", err)
		}
	default:
		return nil, nil
	}
	if len(b) == 0 {
		return nil, fmt.Errorf("empty png")
	}
	if _, err := png.DecodeConfig(bytes.NewReader(b)); err != nil {
		return nil, fmt.Errorf("not a PNG: %w", err)
	}
	return b, nil
}

// overlayImageNames gives the library a fixed order: the C side addresses images by index.
func overlayImageNames(p *goavpipe.OverlayParams) []string {
	names := make([]string, 0, len(p.Images))
	for name := range p.Images {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func overlayInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), n == math.Trunc(n)
	default:
		return 0, false
	}
}

// overlayClockValue decodes a clock slot's value: {"ms": int, "running": bool, "dir": "down"|"up"}.
func overlayClockValue(v any) (*overlayClock, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("wants {ms, running, dir}, got %T", v)
	}
	c := &overlayClock{}
	for k, x := range m {
		switch k {
		case "ms":
			n, ok := overlayInt64(x)
			if !ok || n < 0 {
				return nil, fmt.Errorf("ms must be a non-negative integer")
			}
			c.ms = n
		case "running":
			b, ok := x.(bool)
			if !ok {
				return nil, fmt.Errorf("running must be a bool")
			}
			c.running = b
		case "dir":
			switch x {
			case "down":
			case "up":
				c.up = true
			default:
				return nil, fmt.Errorf("dir must be down or up")
			}
		default:
			return nil, fmt.Errorf("unknown clock key %q", k)
		}
	}
	return c, nil
}

// overlayColorOK accepts ffmpeg colour syntax (names, 0xRRGGBB[AA], @alpha) and nothing the filter graph parser
// reads as structure.
func overlayColorOK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '@', r == '.', r == '#':
		default:
			return false
		}
	}
	return true
}

// overlayRows validates p and resolves each event's Data against the slots.
func overlayRows(p *goavpipe.OverlayParams) ([]overlayRow, error) {
	if p.Timescale <= 0 || p.Timescale > math.MaxInt32 {
		return nil, fmt.Errorf("invalid timescale %d", p.Timescale)
	}
	if len(p.Slots) == 0 {
		return nil, fmt.Errorf("no slots")
	}

	images := make(map[string]int, len(p.Images))
	for i, name := range overlayImageNames(p) {
		if name == "" || len(p.Images[name]) == 0 {
			return nil, fmt.Errorf("image %q is empty", name)
		}
		images[name] = i
	}

	index := make(map[string]int, len(p.Slots))
	for i, s := range p.Slots {
		if s.Name == "" {
			return nil, fmt.Errorf("slot %d has no name", i)
		}
		if _, dup := index[s.Name]; dup {
			return nil, fmt.Errorf("duplicate slot %q", s.Name)
		}
		index[s.Name] = i
		if _, ok := overlaySlotTypes[s.Type]; !ok {
			return nil, fmt.Errorf("slot %q: unknown type %q", s.Name, s.Type)
		}
		if _, ok := overlayClockFormats[s.Format]; !ok {
			return nil, fmt.Errorf("slot %q: invalid clock format %q", s.Name, s.Format)
		}
		if s.X < 0 || s.X > 1 || s.Y < 0 || s.Y > 1 {
			return nil, fmt.Errorf("slot %q: x and y must be within 0..1", s.Name)
		}
		if s.Size <= 0 || s.Size > 1 {
			return nil, fmt.Errorf("slot %q: size must be within 0..1", s.Name)
		}
		if s.Fade < 0 || s.Fade > math.MaxInt32 {
			return nil, fmt.Errorf("slot %q: invalid fade %d", s.Name, s.Fade)
		}
		if s.Type == goavpipe.OverlaySlotImage {
			if _, ok := images[s.Default]; s.Default != "" && !ok {
				return nil, fmt.Errorf("slot %q: default image %q is not in the library", s.Name, s.Default)
			}
		} else if s.Font == "" {
			return nil, fmt.Errorf("slot %q: a font file is required", s.Name)
		}
		if !overlayColorOK(s.Color) {
			return nil, fmt.Errorf("slot %q: invalid color %q", s.Name, s.Color)
		}
		if _, ok := overlayAligns[s.Align]; !ok {
			return nil, fmt.Errorf("slot %q: invalid align %q", s.Name, s.Align)
		}
	}

	rows := make([]overlayRow, len(p.Events))
	for i, e := range p.Events {
		if i > 0 && e.Pts < p.Events[i-1].Pts {
			return nil, fmt.Errorf("events not sorted by pts at index %d", i)
		}
		rows[i].text = make([]*string, len(p.Slots))
		rows[i].clock = make([]*overlayClock, len(p.Slots))
		rows[i].image = make([]int, len(p.Slots))
		rows[i].png = make([][]byte, len(p.Slots))
		for j := range rows[i].image {
			rows[i].image[j] = overlayImageDefault
		}
		for name, v := range e.Data {
			j, ok := index[name]
			if !ok {
				return nil, fmt.Errorf("event %d: unknown slot %q", i, name)
			}
			if v == nil {
				if p.Slots[j].Type == goavpipe.OverlaySlotImage {
					rows[i].image[j] = overlayImageHidden
				}
				continue
			}
			switch p.Slots[j].Type {
			case goavpipe.OverlaySlotImage:
				if b, err := overlayInlinePNG(v); err != nil {
					return nil, fmt.Errorf("event %d: slot %q: %w", i, name, err)
				} else if b != nil {
					rows[i].image[j] = overlayImageInline
					rows[i].png[j] = b
					continue
				}
				name, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("event %d: slot %q wants an image name or PNG, got %T", i, name, v)
				}
				k, ok := images[name]
				if !ok {
					return nil, fmt.Errorf("event %d: image %q is not in the library", i, name)
				}
				rows[i].image[j] = k
			case goavpipe.OverlaySlotClock:
				c, err := overlayClockValue(v)
				if err != nil {
					return nil, fmt.Errorf("event %d: slot %q: %w", i, name, err)
				}
				rows[i].clock[j] = c
			default:
				tv, ok := v.(string)
				if !ok {
					return nil, fmt.Errorf("event %d: slot %q wants a string, got %T", i, name, v)
				}
				rows[i].text[j] = &tv
			}
		}
	}
	return rows, nil
}

// setCOverlay copies params.Overlay into cparams. C copies every string, so the C strings made here are freed
// before returning.
func setCOverlay(cparams *C.xcparams_t, p *goavpipe.OverlayParams) error {
	rows, err := overlayRows(p)
	if err != nil {
		return err
	}

	names := overlayImageNames(p)
	if C.overlay_alloc(cparams, C.int64_t(p.Timescale), C.int(len(p.Slots)), C.int(len(p.Events))) == nil {
		return EAV_MEM_ALLOC
	}
	images := make(map[string]int, len(names))
	for i, name := range names {
		b := p.Images[name]
		idx := C.overlay_add_image(cparams, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.int(len(b)))
		if int(idx) != i {
			return EAV_MEM_ALLOC
		}
		images[name] = i
	}
	for i, s := range p.Slots {
		font := C.CString(s.Font)
		color := C.CString(s.Color)
		defaultImage := overlayImageHidden
		if s.Default != "" {
			defaultImage = images[s.Default]
		}
		rc := C.overlay_set_slot(cparams, C.int(i), overlaySlotTypes[s.Type],
			C.float(s.X), C.float(s.Y), C.float(s.Size), C.float(s.H), overlayAligns[s.Align], font, color,
			overlayClockFormats[s.Format], C.int(defaultImage), C.int(s.Fade))
		C.free(unsafe.Pointer(font))
		C.free(unsafe.Pointer(color))
		if err := avpipeError(rc); err != nil {
			return err
		}
	}
	for i, e := range p.Events {
		rc := C.overlay_set_event(cparams, C.int(i), C.int64_t(e.Seq), C.int64_t(e.Pts), cBool(e.Hidden))
		if err := avpipeError(rc); err != nil {
			return err
		}
		if err := setCOverlayValues(cparams, i, rows[i]); err != nil {
			return err
		}
	}
	C.overlay_set_through(cparams, C.int64_t(p.ThroughPts))
	return nil
}

// setCOverlayValues sets the slot values of event i from its resolved row.
func setCOverlayValues(cparams *C.xcparams_t, i int, row overlayRow) error {
	for j, t := range row.text {
		if t == nil {
			continue
		}
		cs := C.CString(*t)
		rc := C.overlay_set_event_text(cparams, C.int(i), C.int(j), cs)
		C.free(unsafe.Pointer(cs))
		if err := avpipeError(rc); err != nil {
			return err
		}
	}
	for j, c := range row.clock {
		if c == nil {
			continue
		}
		rc := C.overlay_set_event_clock(cparams, C.int(i), C.int(j), C.int64_t(c.ms),
			cBool(c.running), cBool(c.up))
		if err := avpipeError(rc); err != nil {
			return err
		}
	}
	for j, k := range row.image {
		if k == overlayImageDefault {
			continue
		}
		if k == overlayImageInline {
			b := row.png[j]
			idx := C.overlay_add_image(cparams, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.int(len(b)))
			if idx < 0 {
				return EAV_MEM_ALLOC
			}
			k = int(idx)
		}
		if err := avpipeError(C.overlay_set_event_image(cparams, C.int(i), C.int(j), C.int(k))); err != nil {
			return err
		}
	}
	return nil
}

func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

// overlaySourceHandle owns the OverlayEventSource while a transcode runs. The C side holds the cgo handle; cancel
// closes the source to unblock a pending call without deleting a handle an in-flight call may still use, and
// release deletes it once the job is over.
type overlaySourceHandle struct {
	params      *goavpipe.OverlayParams // the layout, which the fetched events are resolved against
	url         string
	handle      cgo.Handle
	cancelled   atomic.Bool
	closeOnce   sync.Once
	closeErr    error
	releaseOnce sync.Once
	fetches     atomic.Int64
	events      atomic.Int64
}

// newOverlaySource wraps params.Overlay.Source, or returns nil when there is none.
func newOverlaySource(params *goavpipe.XcParams) *overlaySourceHandle {
	if params.Overlay == nil || params.Overlay.Source == nil {
		return nil
	}
	h := &overlaySourceHandle{params: params.Overlay, url: params.Url}
	h.handle = cgo.NewHandle(h)
	return h
}

func (h *overlaySourceHandle) close() error {
	h.closeOnce.Do(func() {
		h.closeErr = h.params.Source.Close()
	})
	return h.closeErr
}

func (h *overlaySourceHandle) cancel() {
	h.cancelled.Store(true)
	_ = h.close()
}

func (h *overlaySourceHandle) release() {
	h.releaseOnce.Do(func() {
		if err := h.close(); err != nil {
			goavpipe.Log.Warn("overlay event source close failed", "err", err, "url", h.url)
		}
		h.handle.Delete()
		goavpipe.Log.Info("overlay event source released", "url", h.url,
			"fetches", h.fetches.Load(), "events", h.events.Load(), "cancelled", h.cancelled.Load())
	})
}

// initCOverlaySource points the C overlay at the Go source.
func initCOverlaySource(cparams *C.xcparams_t, h *overlaySourceHandle) {
	if h != nil {
		C.init_overlay_reader(cparams, C.uintptr_t(h.handle))
	}
}

//export AVPipeOverlayEvents
func AVPipeOverlayEvents(handle C.uintptr_t, cparams *C.xcparams_t, afterSeq C.int64_t, atLeast C.int64_t) C.int {
	return C.int(overlayEvents(uintptr(handle), cparams, int64(afterSeq), int64(atLeast)))
}

// overlayEvents is AVPipeOverlayEvents with Go types: it fetches the events after afterSeq through atLeast, appends
// them to the C params and sets the through pts. 0 on success, OV_READER_CANCELLED after a cancel, -1 on failure.
// A handle that release() has deleted panics in cgo.Handle; that must not reach the C caller.
func overlayEvents(handle uintptr, cparams *C.xcparams_t, afterSeq, atLeast int64) (rc int) {
	defer func() {
		if r := recover(); r != nil {
			goavpipe.Log.Error("AVPipeOverlayEvents()", "reason", "source handle no longer valid", "panic", r)
			rc = -1
		}
	}()

	h, ok := cgo.Handle(handle).Value().(*overlaySourceHandle)
	if !ok {
		goavpipe.Log.Error("AVPipeOverlayEvents()", "reason", "not an overlay source handle")
		return -1
	}
	h.fetches.Add(1)

	events, through, err := h.params.Source.EventsThrough(afterSeq, atLeast)
	if err != nil {
		if h.cancelled.Load() {
			goavpipe.Log.Debug("AVPipeOverlayEvents()", "reason", "cancelled", "err", err, "url", h.url)
			return C.OV_READER_CANCELLED
		}
		goavpipe.Log.Error("AVPipeOverlayEvents()", err, "reason", "event source failed",
			"after_seq", afterSeq, "at_least", atLeast, "url", h.url)
		return -1
	}

	resolved := goavpipe.OverlayParams{
		Timescale: h.params.Timescale, Slots: h.params.Slots, Images: h.params.Images, Events: events}
	rows, err := overlayRows(&resolved)
	if err != nil {
		goavpipe.Log.Error("AVPipeOverlayEvents()", err, "reason", "bad events from the source", "url", h.url)
		return -1
	}
	for i, e := range events {
		if e.Seq <= afterSeq {
			goavpipe.Log.Error("AVPipeOverlayEvents()", "reason", "event seq not after the last one",
				"seq", e.Seq, "after_seq", afterSeq, "url", h.url)
			return -1
		}
		idx := C.overlay_append_event(cparams, C.int64_t(e.Seq), C.int64_t(e.Pts), cBool(e.Hidden))
		if idx < 0 {
			return -1
		}
		if err := setCOverlayValues(cparams, int(idx), rows[i]); err != nil {
			goavpipe.Log.Error("AVPipeOverlayEvents()", err, "reason", "failed to set event values", "url", h.url)
			return -1
		}
	}
	C.overlay_set_through(cparams, C.int64_t(through))
	h.events.Add(int64(len(events)))
	goavpipe.Log.Debug("AVPipeOverlayEvents()", "url", h.url, "after_seq", afterSeq, "at_least", atLeast,
		"events", len(events), "through_pts", through)
	return 0
}
