package mpegtsxc

import "testing"

func TestRtpGapDetector(t *testing.T) {
	d := newRtpGapDetector(64, 90000) // 1s ts gap

	// Init + smooth progress.
	if d.Update(100, 10000) {
		t.Fatal("init flagged a discontinuity")
	}
	if d.Update(101, 10300) {
		t.Fatal("smooth step flagged a discontinuity")
	}

	// Seq wrap 65535 -> 0 is not a gap.
	d2 := newRtpGapDetector(64, 90000)
	d2.Update(65534, 1000)
	if d2.Update(65535, 1300) || d2.Update(0, 1600) || d2.Update(1, 1900) {
		t.Fatal("seq wrap flagged a discontinuity")
	}

	// Ts wrap 2^32 boundary is not a gap.
	d3 := newRtpGapDetector(64, 90000)
	d3.Update(10, 0xFFFFF000)
	if d3.Update(11, 0x00000500) {
		t.Fatal("ts wrap flagged a discontinuity")
	}

	// Seq jump beyond the threshold is a gap.
	if !d.Update(1000, 10600) {
		t.Fatal("seq jump not flagged")
	}
	if d.Discontinuities() != 1 {
		t.Fatalf("disc count = %d, want 1", d.Discontinuities())
	}
	// Recovers after the jump.
	if d.Update(1001, 10900) {
		t.Fatal("post-jump step flagged a discontinuity")
	}

	// Ts jump beyond the threshold is a gap (5s jump).
	if !d.Update(1002, 10900+5*90000) {
		t.Fatal("ts jump not flagged")
	}
	if d.Discontinuities() != 2 {
		t.Fatalf("disc count = %d, want 2", d.Discontinuities())
	}
}
