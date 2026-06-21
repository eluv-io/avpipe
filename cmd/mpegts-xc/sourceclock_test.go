package main

import (
	"math"
	"testing"
	"time"
)

// feedSourceClock simulates a source clock running at (1+ppm) at 27 MHz, sampled every
// `interval` of local time
// - optional ±jitter on the arrival time
// Returns the clock plus the last local time fed.
func feedSourceClock(ppm float64, interval, jitter time.Duration, n int) (*sourceClock, time.Time) {
	sc := newSourceClock()
	base := time.Unix(0, 0)
	rate := float64(pcrClockHz) * (1 + ppm*1e-6)
	var last time.Time
	for i := 0; i < n; i++ {
		lt := time.Duration(i) * interval
		// PCR value tracks true source time; arrival jitter affects only the observed local time, like network/pacing jitter.
		pcr := uint64(rate * lt.Seconds())
		j := time.Duration(0)
		if jitter > 0 {
			// deterministic pseudo-jitter, zero-mean
			j = time.Duration((i%7 - 3)) * jitter / 3
		}
		arrival := base.Add(lt + j)
		sc.Update(pcr, arrival)
		last = base.Add(lt)
	}
	return sc, last
}

func TestSourceClockLocksClean(t *testing.T) {
	const ppm = 30.0
	sc, last := feedSourceClock(ppm, 40*time.Millisecond, 0, 400)

	stc, ok := sc.Now(last)
	if !ok {
		t.Fatal("clock never locked on a clean source")
	}
	// The phase-lock requirement: Now() tracks the source STC. Match the true
	// source STC at `last` within ~1 ms.
	want := int64(float64(pcrClockHz) * (1 + ppm*1e-6) * last.Sub(time.Unix(0, 0)).Seconds())
	if d := stc - want; d > 27_000 || d < -27_000 {
		t.Fatalf("STC estimate off by %d ticks (> 1 ms)", d)
	}
	// Frequency only needs to be sane (used for short inter-sample extrapolation).
	if gotPPM := (sc.freq/float64(pcrClockHz) - 1) * 1e6; math.Abs(gotPPM) > 100 {
		t.Fatalf("frequency estimate %.2f ppm looks unstable", gotPPM)
	}
}

func TestSourceClockLocksWithSmallJitter(t *testing.T) {
	// ±1 ms arrival jitter (representative of a clean encoder/network).
	sc, last := feedSourceClock(20.0, 40*time.Millisecond, time.Millisecond, 600)
	stc, ok := sc.Now(last)
	if !ok {
		t.Fatalf("clock never locked with small jitter (emaErr=%.1f ms)", sc.emaAbsErr/27000)
	}
	// Within ~2 ms of true STC despite ±1 ms arrival jitter.
	want := int64(float64(pcrClockHz) * (1 + 20*1e-6) * last.Sub(time.Unix(0, 0)).Seconds())
	if d := stc - want; d > 54_000 || d < -54_000 {
		t.Fatalf("STC estimate off by %d ticks (> 2 ms) with jitter", d)
	}
}

func TestSourceClockDiscontinuity(t *testing.T) {
	sc := newSourceClock()
	base := time.Unix(0, 0)
	rate := float64(pcrClockHz)
	for i := 0; i < 200; i++ {
		lt := time.Duration(i) * 40 * time.Millisecond
		sc.Update(uint64(rate*lt.Seconds()), base.Add(lt))
	}
	if _, ok := sc.Now(base.Add(8 * time.Second)); !ok {
		t.Fatal("did not lock before discontinuity")
	}
	// Jump the source STC by 5 s — a discontinuity, not jitter.
	lt := time.Duration(200) * 40 * time.Millisecond
	sc.Update(uint64(rate*(lt.Seconds()+5)), base.Add(lt))
	if !sc.TakeDiscontinuity() {
		t.Fatal("discontinuity not flagged")
	}
	if sc.locked {
		t.Fatal("clock should drop lock on discontinuity")
	}
}
