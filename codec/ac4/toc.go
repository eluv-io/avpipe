// Package ac4 parses AC-4 (ETSI TS 103 190-1/-2) bitstream structures that are not
// available from container-level metadata. Its focus is the ac4_toc() at the start of
// every raw_ac4_frame, in particular b_iframe_global — the authoritative I-frame
// (sync-sample) flag, which container sync signaling (stss / trun-trex flags) does not
// always reflect faithfully.
//
// The package is container-agnostic: ParseTOC operates on a raw_ac4_frame (an MP4 AC-4
// sample, or the payload of one ac4_syncframe), and ScanSyncFrames handles the
// sync-frame framing used by .ac4 elementary streams (and, in future, MPEG-2 TS). It
// depends only on the mp4ff bit reader, never on the MP4 container layer.
package ac4

import (
	"bytes"

	mp4bits "github.com/Eyevinn/mp4ff/bits"
	"github.com/eluv-io/errors-go"
)

// DefaultMaxIFrames is the default cap on the number of I-frames a scan collects per
// track / elementary stream. Zero means unlimited.
const DefaultMaxIFrames = 20

// minTOCBytes is the largest number of leading bytes ParseTOC can consume before
// reaching b_iframe_global: bitstream_version(2) + sequence_counter(10) +
// b_wait_frames(1) + wait_frames(3) + reserved(2) + fs_index(1) + frame_rate_index(4) +
// b_iframe_global(1) = 24 bits = 3 bytes. (This ignores the bitstream_version==3
// variable_bits escape, which only lengthens the prefix and is guarded by AccError.)
const minTOCBytes = 3

// TOC holds the leading fields of an AC-4 ac4_toc(), decoded only as far as
// b_iframe_global (ETSI TS 103 190-1 Table 4). The rest of the ac4_toc is not parsed.
type TOC struct {
	// BitstreamVersion is ac4_toc.bitstream_version (after the variable_bits escape
	// when the initial 2-bit value is 3).
	BitstreamVersion int
	// FSIndex is ac4_toc.fs_index (0 => 44.1 kHz base, 1 => 48 kHz base).
	FSIndex int
	// FrameRateIndex is ac4_toc.frame_rate_index (see ETSI Table 83).
	FrameRateIndex int
	// IFrameGlobal is ac4_toc.b_iframe_global: true iff this frame is an AC-4 I-frame
	// (a sync sample / random access point).
	IFrameGlobal bool
}

// ParseTOC parses ac4_toc() from the start of a raw_ac4_frame. The AC-4 bitstream is
// MSB-first (uimsbf). Only the prefix up to and including b_iframe_global is decoded.
func ParseTOC(frame []byte) (TOC, error) {
	e := errors.T("ac4.ParseTOC", errors.K.Invalid.Default())
	if len(frame) < minTOCBytes {
		return TOC{}, e("reason", "frame too short for ac4_toc", "len", len(frame), "min", minTOCBytes)
	}
	r := mp4bits.NewReader(bytes.NewReader(frame))

	var t TOC
	bitstreamVersion := int(r.Read(2))
	if bitstreamVersion == 3 {
		bitstreamVersion += int(readVariableBits(r, 2))
	}
	t.BitstreamVersion = bitstreamVersion

	_ = r.Read(10) // sequence_counter

	if r.ReadFlag() { // b_wait_frames
		waitFrames := r.Read(3)
		if waitFrames > 0 {
			_ = r.Read(2) // reserved
		}
	}

	t.FSIndex = int(r.Read(1))
	t.FrameRateIndex = int(r.Read(4))
	t.IFrameGlobal = r.ReadFlag() // b_iframe_global

	if err := r.AccError(); err != nil {
		return TOC{}, e(err, "reason", "bitstream read error")
	}
	return t, nil
}

// readVariableBits implements variable_bits(n) from ETSI TS 103 190-1 Table 3: read n
// bits into value, and while the following continuation flag is set, shift value left
// by n, add (1<<n), and read another n-bit group.
func readVariableBits(r *mp4bits.Reader, n int) uint64 {
	var value uint64
	for {
		value += uint64(r.Read(n))
		if !r.ReadFlag() { // b_read_more
			break
		}
		value <<= uint(n)
		value += 1 << uint(n)
	}
	return value
}
