// Package anctest builds MPEG-TS test inputs that carry a SMPTE ST 2038 ancillary data stream, for tests of
// avpipe's ANC stat. The PES payloads come from anc.EncodePES (common-go). ffmpeg's CLI cannot create such a
// stream (its raw data demuxer has no codec id), so the muxing goes through libavformat.
package anctest

/*
#cgo pkg-config: libavformat libavcodec libavutil
#include <stdlib.h>
#include "tsgen.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"unsafe"
)

// Written is one ANC PES packet that WriteTS put in the output.
type Written struct {
	Pts     int64 // 90 kHz
	Payload []byte
}

// WriteTS copies the video and audio streams of src into dst as MPEG-TS and adds an ST 2038 data stream (PES
// stream_id 0xBD). payload is called once per video frame, in presentation order, with the frame's index and its
// pts in 90 kHz units, and returns the PES payload to carry at that pts, or nil for none. Each packet is written
// next to its frame, as an encoder muxes it: right after the frame's video packet, or with B-frames once the
// video's dts has reached the frame's pts. The packets written are returned in pts order, which is also the order
// a demuxer delivers them in.
func WriteTS(src, dst string, payload func(i int, pts int64) []byte) ([]Written, error) {
	return writeTS(src, dst, false, payload)
}

// WriteTSUnmarked is WriteTS with the data stream written as private data without the VANC registration, as an
// encoder with a vendor descriptor would: a demuxer sees a generic private data stream, which avpipe reads as
// ANC only when told its id (XcParams.AncStreamId).
func WriteTSUnmarked(src, dst string, payload func(i int, pts int64) []byte) ([]Written, error) {
	return writeTS(src, dst, true, payload)
}

func writeTS(src, dst string, unmarked bool, payload func(i int, pts int64) []byte) ([]Written, error) {
	csrc, cdst := C.CString(src), C.CString(dst)
	defer C.free(unsafe.Pointer(csrc))
	defer C.free(unsafe.Pointer(cdst))

	cUnmarked := C.int(0)
	if unmarked {
		cUnmarked = 1
	}
	// First pass: the video's pts, to number the frames in presentation order
	scan := C.tsgen_open(csrc, nil, 0)
	if scan == nil {
		return nil, fmt.Errorf("anctest: cannot open %s", src)
	}
	var ptss []int64
	for {
		var pts, dts C.int64_t
		rc := C.tsgen_next_video(scan, &pts, &dts)
		if rc < 0 {
			C.tsgen_close(scan)
			return nil, errors.New("anctest: reading the video")
		}
		if rc == 0 {
			break
		}
		if int64(pts) != math.MinInt64 { // AV_NOPTS_VALUE
			ptss = append(ptss, int64(pts))
		}
	}
	C.tsgen_close(scan)
	sort.Slice(ptss, func(a, b int) bool { return ptss[a] < ptss[b] })
	index := make(map[int64]int, len(ptss))
	for i, pts := range ptss {
		index[pts] = i
	}

	g := C.tsgen_open(csrc, cdst, cUnmarked)
	if g == nil {
		return nil, fmt.Errorf("anctest: cannot open %s or %s", src, dst)
	}
	var out []Written
	write := func(pts int64) error {
		p := payload(index[pts], pts)
		if len(p) == 0 {
			return nil
		}
		if C.tsgen_write_anc(g, C.int64_t(pts), (*C.uint8_t)(unsafe.Pointer(&p[0])), C.int(len(p))) < 0 {
			return fmt.Errorf("anctest: writing the ANC packet at pts %d", pts)
		}
		out = append(out, Written{Pts: pts, Payload: p})
		return nil
	}

	// Second pass: copy the media, which comes in decode order. A frame's packet is written once the video's dts
	// has reached the frame's pts: at once without B-frames, a frame or two later with them. That keeps the data
	// stream in pts order, which the muxer needs, and every packet next to its frame.
	var pending []int64 // pts of the frames whose packet is not written yet, ascending
	for {
		var cpts, cdts C.int64_t
		rc := C.tsgen_next_video(g, &cpts, &cdts)
		if rc < 0 {
			C.tsgen_close(g)
			return nil, errors.New("anctest: copying the video")
		}
		if rc == 0 {
			break
		}
		pts, dts := int64(cpts), int64(cdts)
		if pts == math.MinInt64 {
			continue
		}
		if dts == math.MinInt64 {
			dts = pts
		}
		at := sort.Search(len(pending), func(k int) bool { return pending[k] >= pts })
		pending = append(pending, 0)
		copy(pending[at+1:], pending[at:])
		pending[at] = pts
		for len(pending) > 0 && pending[0] <= dts {
			if err := write(pending[0]); err != nil {
				C.tsgen_close(g)
				return nil, err
			}
			pending = pending[1:]
		}
	}
	for _, pts := range pending {
		if err := write(pts); err != nil {
			C.tsgen_close(g)
			return nil, err
		}
	}
	if C.tsgen_close(g) < 0 {
		return nil, errors.New("anctest: closing the output")
	}
	return out, nil
}
