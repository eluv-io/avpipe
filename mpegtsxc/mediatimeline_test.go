package mpegtsxc

import "testing"

func TestMediaTimelineUnwrap(t *testing.T) {
	const mod = int64(1) << 33

	m := &mediaTimeline{}

	// First value initializes the line.
	if got := m.unwrap(1000); got != 1000 {
		t.Fatalf("init: got %d, want 1000", got)
	}
	// Forward progress.
	if got := m.unwrap(2000); got != 2000 {
		t.Fatalf("forward: got %d, want 2000", got)
	}
	// Small backwards step (e.g. DTS just behind the PCR) stays near the line.
	if got := m.unwrap(1500); got != 1500 {
		t.Fatalf("backwards: got %d, want 1500", got)
	}

	// Wrap: a stream that starts just before the 33-bit limit and crosses it.
	m2 := &mediaTimeline{}
	nearMax := mod - 90000 // 1s before the wrap
	if got := m2.unwrap(nearMax); got != nearMax {
		t.Fatalf("nearMax: got %d, want %d", got, nearMax)
	}
	if got := m2.unwrap(90000); got != mod+90000 {
		t.Fatalf("wrap: got %d, want %d", got, mod+90000)
	}
	// Late-arriving pre-wrap value (e.g. the lagging video leg) maps just below the
	// line, not 26.5h back.
	if got := m2.unwrap(mod - 45000); got != mod-45000 {
		t.Fatalf("pre-wrap: got %d, want %d", got, mod-45000)
	}
	if got := m2.unwrap(180000); got != mod+180000 {
		t.Fatalf("post-wrap: got %d, want %d", got, mod+180000)
	}
}

func TestMediaTimelineConcurrentReaders(t *testing.T) {
	// The input (PCR) and video (DTS) legs interleave; the video lags by seconds.
	m := &mediaTimeline{}
	pcr := int64(1_000_000)
	dts := int64(1_030_000) // DTS runs a bit ahead of PCR

	m.unwrap(pcr)
	for i := 0; i < 100000; i++ {
		pcr += 3000
		dts += 3000
		if got := m.unwrap(pcr % (1 << 33)); got != pcr {
			t.Fatalf("pcr step %d: got %d, want %d", i, got, pcr)
		}
		if got := m.unwrap(dts % (1 << 33)); got != dts {
			t.Fatalf("dts step %d: got %d, want %d", i, got, dts)
		}
	}
}
