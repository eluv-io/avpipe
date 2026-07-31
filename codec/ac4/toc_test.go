package ac4

import (
	"bytes"
	"testing"

	mp4bits "github.com/Eyevinn/mp4ff/bits"
	"github.com/eluv-io/errors-go"
)

// buildTOC constructs a byte-aligned ac4_toc prefix (no bitstream_version escape).
// waitFrames < 0 encodes b_wait_frames = 0; waitFrames >= 0 encodes b_wait_frames = 1
// with that wait_frames value (and the trailing reserved 2 bits when > 0).
func buildTOC(t *testing.T, bv, seq, fsIndex, fri uint, waitFrames int, iframe bool) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := mp4bits.NewWriter(&buf)
	w.Write(bv, 2)   // bitstream_version (< 3, no escape)
	w.Write(seq, 10) // sequence_counter
	if waitFrames < 0 {
		w.Write(0, 1) // b_wait_frames = 0
	} else {
		w.Write(1, 1)                // b_wait_frames = 1
		w.Write(uint(waitFrames), 3) // wait_frames
		if waitFrames > 0 {
			w.Write(0, 2) // reserved
		}
	}
	w.Write(fsIndex, 1) // fs_index
	w.Write(fri, 4)     // frame_rate_index
	if iframe {
		w.Write(1, 1)
	} else {
		w.Write(0, 1)
	}
	w.Flush()
	if err := w.AccError(); err != nil {
		t.Fatalf("building toc: %v", err)
	}
	return buf.Bytes()
}

func TestParseTOC(t *testing.T) {
	cases := []struct {
		name       string
		bv         uint
		seq        uint
		fsIndex    uint
		fri        uint
		waitFrames int
		iframe     bool
	}{
		{"iframe_no_wait", 2, 0, 1, 6, -1, true},
		{"non_iframe_no_wait", 2, 0, 1, 6, -1, false},
		{"wait_frames_zero", 1, 123, 0, 3, 0, true},
		{"wait_frames_nonzero_reserved", 2, 1023, 1, 13, 4, true},
		{"bv0_25fps", 0, 512, 1, 2, -1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frame := buildTOC(t, c.bv, c.seq, c.fsIndex, c.fri, c.waitFrames, c.iframe)
			got, err := ParseTOC(frame)
			if err != nil {
				t.Fatalf("ParseTOC: %v", err)
			}
			if got.BitstreamVersion != int(c.bv) {
				t.Errorf("BitstreamVersion = %d, want %d", got.BitstreamVersion, c.bv)
			}
			if got.FSIndex != int(c.fsIndex) {
				t.Errorf("FSIndex = %d, want %d", got.FSIndex, c.fsIndex)
			}
			if got.FrameRateIndex != int(c.fri) {
				t.Errorf("FrameRateIndex = %d, want %d", got.FrameRateIndex, c.fri)
			}
			if got.IFrameGlobal != c.iframe {
				t.Errorf("IFrameGlobal = %v, want %v", got.IFrameGlobal, c.iframe)
			}
		})
	}
}

// TestParseTOCBitstreamVersionEscape exercises the bitstream_version == 3 path, where
// the 2-bit field is followed by variable_bits(2).
func TestParseTOCBitstreamVersionEscape(t *testing.T) {
	var buf bytes.Buffer
	w := mp4bits.NewWriter(&buf)
	w.Write(3, 2) // bitstream_version = 3 -> escape
	// variable_bits(2): first 2-bit group = 1, b_read_more = 1;
	// second 2-bit group = 0, b_read_more = 0.
	// value = 1; then value = (1<<2) + (1<<2) + 0 = 8. bitstream_version = 3 + 8 = 11.
	w.Write(1, 2)  // group 0
	w.Write(1, 1)  // b_read_more = 1
	w.Write(0, 2)  // group 1
	w.Write(0, 1)  // b_read_more = 0
	w.Write(0, 10) // sequence_counter
	w.Write(0, 1)  // b_wait_frames = 0
	w.Write(1, 1)  // fs_index
	w.Write(6, 4)  // frame_rate_index
	w.Write(1, 1)  // b_iframe_global
	w.Flush()
	if err := w.AccError(); err != nil {
		t.Fatalf("building toc: %v", err)
	}

	got, err := ParseTOC(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseTOC: %v", err)
	}
	if got.BitstreamVersion != 11 {
		t.Errorf("BitstreamVersion = %d, want 11", got.BitstreamVersion)
	}
	if !got.IFrameGlobal {
		t.Errorf("IFrameGlobal = false, want true")
	}
	if got.FrameRateIndex != 6 {
		t.Errorf("FrameRateIndex = %d, want 6", got.FrameRateIndex)
	}
}

func TestParseTOCShortFrame(t *testing.T) {
	_, err := ParseTOC([]byte{0x80, 0x00})
	if err == nil {
		t.Fatal("expected error for short frame")
	}
	if !errors.IsKind(errors.K.Invalid, err) {
		t.Errorf("kind = %v, want Invalid", err)
	}
}
