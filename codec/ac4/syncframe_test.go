package ac4

import (
	"bytes"
	"testing"
)

// wrapSyncFrame wraps a raw_ac4_frame in an ac4_syncframe (16-bit frame_size path).
func wrapSyncFrame(raw []byte, hasCRC bool) []byte {
	var b bytes.Buffer
	if hasCRC {
		b.Write([]byte{0xAC, 0x41})
	} else {
		b.Write([]byte{0xAC, 0x40})
	}
	b.Write([]byte{byte(len(raw) >> 8), byte(len(raw))}) // frame_size
	b.Write(raw)
	if hasCRC {
		b.Write([]byte{0x00, 0x00}) // dummy crc_word (not validated)
	}
	return b.Bytes()
}

func TestScanSyncFramesAndIFrameIndices(t *testing.T) {
	f0 := buildTOC(t, 2, 0, 1, 6, -1, true)  // I-frame
	f1 := buildTOC(t, 2, 1, 1, 6, -1, false) // not
	f2 := buildTOC(t, 2, 2, 1, 6, -1, true)  // I-frame (CRC variant)

	var es bytes.Buffer
	es.Write(wrapSyncFrame(f0, false))
	es.Write(wrapSyncFrame(f1, false))
	es.Write(wrapSyncFrame(f2, true))

	// Scan all frames.
	var seen []SyncFrame
	if err := ScanSyncFrames(bytes.NewReader(es.Bytes()), func(sf SyncFrame) (bool, error) {
		seen = append(seen, sf)
		return false, nil
	}); err != nil {
		t.Fatalf("ScanSyncFrames: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("scanned %d frames, want 3", len(seen))
	}
	if !seen[2].HasCRC || seen[0].HasCRC {
		t.Errorf("HasCRC flags wrong: %v", []bool{seen[0].HasCRC, seen[1].HasCRC, seen[2].HasCRC})
	}

	// All I-frames.
	idx, err := IFrameIndices(bytes.NewReader(es.Bytes()), 0)
	if err != nil {
		t.Fatalf("IFrameIndices: %v", err)
	}
	if want := []int{0, 2}; !equalInts(idx, want) {
		t.Errorf("IFrameIndices(0) = %v, want %v", idx, want)
	}

	// Bounded to the first I-frame (early stop).
	idx, err = IFrameIndices(bytes.NewReader(es.Bytes()), 1)
	if err != nil {
		t.Fatalf("IFrameIndices(1): %v", err)
	}
	if want := []int{0}; !equalInts(idx, want) {
		t.Errorf("IFrameIndices(1) = %v, want %v", idx, want)
	}
}

func TestScanSyncFramesBadSyncWord(t *testing.T) {
	err := ScanSyncFrames(bytes.NewReader([]byte{0xDE, 0xAD, 0x00, 0x03, 0, 0, 0}), func(SyncFrame) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("expected error on bad sync word")
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
