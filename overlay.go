package avpipe

/*
#include <stdlib.h>
#include "avpipe_xc.h"
#include "avpipe_overlay.h"
*/
import "C"

import (
	"bytes"
	"fmt"
	"image/png"
	"math"
	"sort"
	"unsafe"

	"github.com/eluv-io/avpipe/goavpipe"
)

const (
	overlayImageHidden  = -1 // C OV_IMAGE_HIDDEN
	overlayImageDefault = -2 // C OV_IMAGE_DEFAULT
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
	"game":    C.int(C.ov_clock_game),
	"shot":    C.int(C.ov_clock_shot),
}

var overlayTimeouts = map[string]C.int{
	"":                          C.int(C.ov_timeout_fail),
	goavpipe.OverlayTimeoutFail: C.int(C.ov_timeout_fail),
	goavpipe.OverlayTimeoutHold: C.int(C.ov_timeout_hold),
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

// overlayTimescale and overlayWaitMs apply the defaults.
func overlayTimescale(p *goavpipe.OverlayParams) int64 {
	if p.Timescale == 0 {
		return goavpipe.OverlayDefaultTimescale
	}
	return p.Timescale
}

func overlayWaitMs(p *goavpipe.OverlayParams) int64 {
	if p.WaitMs == 0 {
		return goavpipe.OverlayDefaultWaitMs
	}
	return p.WaitMs
}

// overlayCheckLayout validates the layout and returns the library index of every image name.
func overlayCheckLayout(p *goavpipe.OverlayParams) (map[string]int, error) {
	if ts := overlayTimescale(p); ts <= 0 || ts > math.MaxInt32 {
		return nil, fmt.Errorf("invalid timescale %d", ts)
	}
	if p.WaitMs < 0 || p.WaitMs > math.MaxInt32 {
		return nil, fmt.Errorf("invalid wait_ms %d", p.WaitMs)
	}
	if _, ok := overlayTimeouts[p.OnTimeout]; !ok {
		return nil, fmt.Errorf("invalid on_timeout %q", p.OnTimeout)
	}
	if len(p.Slots) == 0 {
		return nil, fmt.Errorf("no slots")
	}

	images := make(map[string]int, len(p.Images))
	for i, name := range overlayImageNames(p) {
		if name == "" || len(p.Images[name]) == 0 {
			return nil, fmt.Errorf("image %q is empty", name)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(p.Images[name])); err != nil {
			return nil, fmt.Errorf("image %q is not a PNG: %w", name, err)
		}
		images[name] = i
	}

	seen := make(map[string]bool, len(p.Slots))
	for i, s := range p.Slots {
		if s.Name == "" {
			return nil, fmt.Errorf("slot %d has no name", i)
		}
		if seen[s.Name] {
			return nil, fmt.Errorf("duplicate slot %q", s.Name)
		}
		seen[s.Name] = true
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
			if s.H <= 0 || s.H > 1 {
				return nil, fmt.Errorf("slot %q: an image slot needs h within 0..1", s.Name)
			}
			if _, ok := images[s.Default]; s.Default != "" && !ok {
				return nil, fmt.Errorf("slot %q: default image %q is not in the library", s.Name, s.Default)
			}
		} else if s.Font == "" {
			return nil, fmt.Errorf("slot %q: a font is required", s.Name)
		}
		if !overlayColorOK(s.Color) {
			return nil, fmt.Errorf("slot %q: invalid color %q", s.Name, s.Color)
		}
		if _, ok := overlayAligns[s.Align]; !ok {
			return nil, fmt.Errorf("slot %q: invalid align %q", s.Name, s.Align)
		}
	}
	return images, nil
}

// setCOverlay copies the layout and the image library into cparams. C copies every string, so the C strings made
// here are freed before returning.
func setCOverlay(cparams *C.xcparams_t, p *goavpipe.OverlayParams) error {
	images, err := overlayCheckLayout(p)
	if err != nil {
		return err
	}
	if C.overlay_alloc(cparams, C.int64_t(overlayTimescale(p)), C.int(len(p.Slots)), C.int64_t(overlayWaitMs(p)),
		overlayTimeouts[p.OnTimeout]) == nil {
		return EAV_MEM_ALLOC
	}
	for _, name := range overlayImageNames(p) {
		b := p.Images[name]
		idx := C.overlay_add_image(cparams, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.int(len(b)))
		if int(idx) != images[name] {
			return EAV_MEM_ALLOC
		}
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
	return nil
}

// overlayEncoder turns OverlayEvents into the C event the compositor applies. Library images are addressed by
// the index setCOverlay gave them; images an event names by hash are fetched once from the source, appended to
// the C library and remembered by hash for the job.
type overlayEncoder struct {
	layout *goavpipe.OverlayParams
	slots  map[string]int // slot name -> index
	images map[string]int // library name -> index
	hashes map[string]int // asset hash -> index, once fetched
}

func newOverlayEncoder(p *goavpipe.OverlayParams) *overlayEncoder {
	e := &overlayEncoder{layout: p, slots: make(map[string]int, len(p.Slots)), images: map[string]int{},
		hashes: map[string]int{}}
	for i, s := range p.Slots {
		e.slots[s.Name] = i
	}
	for i, name := range overlayImageNames(p) {
		e.images[name] = i
	}
	return e
}

// encode fills out from ev. It returns StatusNotYet when an image the event names has not arrived from the
// source yet, and an error for an event that does not fit the layout or an asset the source cannot supply.
func (e *overlayEncoder) encode(src goavpipe.EventSource, cparams *C.xcparams_t, ev *goavpipe.OverlayEvent,
	out *C.ov_event_t) (goavpipe.Status, error) {
	if err := e.layout.ValidateEvent(ev); err != nil {
		return goavpipe.StatusReady, err
	}
	// Fetch every image first: a frame is drawn with all of its event or none of it
	for name, v := range ev.Slots {
		if v.Image == nil || v.Image.Hash == "" {
			continue
		}
		if _, ok := e.hashes[v.Image.Hash]; ok {
			continue
		}
		b, status, err := src.Asset(v.Image.Hash)
		if err != nil {
			return status, fmt.Errorf("slot %q: asset %q: %w", name, v.Image.Hash, err)
		}
		if status != goavpipe.StatusReady {
			return status, nil
		}
		if len(b) == 0 {
			return goavpipe.StatusReady, fmt.Errorf("slot %q: asset %q is empty", name, v.Image.Hash)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(b)); err != nil {
			return goavpipe.StatusReady, fmt.Errorf("slot %q: asset %q is not a PNG: %w", name, v.Image.Hash, err)
		}
		idx := C.overlay_add_image(cparams, (*C.uint8_t)(unsafe.Pointer(&b[0])), C.int(len(b)))
		if idx < 0 {
			return goavpipe.StatusReady, EAV_MEM_ALLOC
		}
		e.hashes[v.Image.Hash] = int(idx)
	}

	nslots := C.int(len(e.layout.Slots))
	C.overlay_event_set(out, C.int64_t(ev.Seq), C.int64_t(ev.Pts), cBool(ev.Hidden))
	for name, v := range ev.Slots {
		j := C.int(e.slots[name])
		var rc C.int
		switch {
		case v.Hide:
			if e.layout.Slots[j].Type == goavpipe.OverlaySlotImage {
				rc = C.overlay_event_set_image(out, nslots, j, C.int(overlayImageHidden))
			}
		case v.Text != nil:
			cs := C.CString(*v.Text)
			rc = C.overlay_event_set_text(out, nslots, j, cs)
			C.free(unsafe.Pointer(cs))
		case v.Clock != nil:
			rc = C.overlay_event_set_clock(out, nslots, j, C.int64_t(v.Clock.Ms), cBool(v.Clock.Running),
				cBool(v.Clock.Up))
		case v.Image != nil && v.Image.Name != "":
			rc = C.overlay_event_set_image(out, nslots, j, C.int(e.images[v.Image.Name]))
		case v.Image != nil:
			rc = C.overlay_event_set_image(out, nslots, j, C.int(e.hashes[v.Image.Hash]))
		}
		if err := avpipeError(rc); err != nil {
			return goavpipe.StatusReady, err
		}
	}
	return goavpipe.StatusReady, nil
}

func cBool(b bool) C.int {
	if b {
		return 1
	}
	return 0
}
