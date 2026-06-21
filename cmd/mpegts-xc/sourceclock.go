package main

import (
	"sync"
	"time"
)

// pcrWrapTicks is the wrap value of a 27 MHz PCR (33-bit base * 300 + 9-bit ext).
const pcrWrapTicks = (int64(1) << 33) * 300

// PLL tuning
const (
	pllKp         = 0.20       // proportional (phase) gain, fraction of phase error
	pllKi         = 0.005      // integral (frequency) gain; freq += Ki*e per sample
	pllEmaAlpha   = 0.10       // smoothing for the |error| estimate used to lock
	pllLockBand   = 270_000    // lock when smoothed |error| < 10 ms (jitter-tolerant)
	pllMinSamples = 25         // minimum PCR samples before declaring lock
	pllDiscBand   = 27_000_000 // |error| > 1 s is treated as a discontinuity, not jitter
)

// sourceClock recovers the source 27 MHz STC from input PCR samples
// - uses a 2nd order PLL
// - it receives PCR and local time from the processor
type sourceClock struct {
	mu sync.Mutex

	base        time.Time // local time origin (first sample)
	initialized bool

	stcEst float64 // estimated source STC (unwrapped, 27 MHz ticks) at tEst
	tEst   float64 // local seconds of the last update
	freq   float64 // source ticks per local second (~27e6 +/- ppm)

	lastRawPcr int64 // last raw PCR value, for wrap detection
	wraps      int64 // number of PCR wraps seen

	samples   int
	emaAbsErr float64 // smoothed |phase error|, for lock detection
	locked    bool
	disc      bool      // a discontinuity occurred since the last TakeDiscontinuity
	lastLog   time.Time // local time of the last periodic debug log
}

func newSourceClock() *sourceClock { return &sourceClock{} }

// ppm returns the estimated source frequency offset vs nominal 27 MHz, in ppm.
func (c *sourceClock) ppm() float64 { return (c.freq/float64(pcrClockHz) - 1) * 1e6 }

func (c *sourceClock) localSeconds(t time.Time) float64 {
	return t.Sub(c.base).Seconds()
}

// Update feeds one input PCR sample (27 MHz) observed at local time now.
func (c *sourceClock) Update(pcr uint64, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	raw := int64(pcr)
	if !c.initialized {
		c.base = now
		c.stcEst = float64(raw)
		c.tEst = 0
		c.freq = float64(pcrClockHz)
		c.lastRawPcr = raw
		c.lastLog = now
		c.initialized = true
		return
	}

	// Unwrap the 27 MHz PCR modulus.
	if raw < c.lastRawPcr-pcrWrapTicks/2 {
		c.wraps++
	}
	c.lastRawPcr = raw
	unwrapped := float64(raw) + float64(c.wraps)*float64(pcrWrapTicks)

	t := c.localSeconds(now)
	dt := t - c.tEst
	if dt <= 0 {
		return
	}
	predicted := c.stcEst + c.freq*dt
	e := unwrapped - predicted

	// A large error is a discontinuity (splice / source restart), not jitter:
	// re-anchor and reset 'locked'
	if e > pllDiscBand || e < -pllDiscBand {
		if c.locked {
			log.Info("mpegts-xc phase-lock lost", "reason", "discontinuity", "errorMs", e/27000)
		}
		c.stcEst = unwrapped
		c.tEst = t
		c.freq = float64(pcrClockHz)
		c.locked = false
		c.samples = 0
		c.emaAbsErr = 0
		c.disc = true
		return
	}

	// 2nd-order PI loop: proportional phase correction plus an integral that
	// accumulates the phase error into the frequency estimate. Keeping the
	// frequency term as Ki*e (not e/dt) avoids amplifying phase jitter by 1/dt.
	c.freq += pllKi * e
	c.stcEst = predicted + pllKp*e
	c.tEst = t

	// Lock on the *smoothed* error so per-sample arrival jitter doesn't prevent
	// locking; the loop's frequency converges even while individual samples jitter.
	absErr := e
	if absErr < 0 {
		absErr = -absErr
	}
	c.emaAbsErr = c.emaAbsErr*(1-pllEmaAlpha) + float64(absErr)*pllEmaAlpha
	c.samples++

	// Lock transition (false -> true) at INFO.
	if !c.locked && c.samples >= pllMinSamples && c.emaAbsErr < pllLockBand {
		c.locked = true
		log.Info("mpegts-xc phase-lock acquired", "freqPPM", c.ppm(), "emaErrMs", c.emaAbsErr/27000)
	}

	// Periodic PLL state at debug, ~every 5s of input.
	if now.Sub(c.lastLog) >= 5*time.Second {
		c.lastLog = now
		log.Debug("mpegts-xc phase-lock pll", "samples", c.samples,
			"emaErrMs", c.emaAbsErr/27000, "freqPPM", c.ppm(), "locked", c.locked)
	}
}

// Now returns the estimated source STC (27 MHz) at local time now, and whether
// the loop is locked. While unlocked the pacer should free-run.
func (c *sourceClock) Now(now time.Time) (int64, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.initialized || !c.locked {
		return 0, false
	}
	return int64(c.stcEst + c.freq*(c.localSeconds(now)-c.tEst)), true
}

// Locked reports whether the loop is currently phase-locked to the source.
func (c *sourceClock) Locked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.locked
}

// TakeDiscontinuity reports and clears whether a source discontinuity occurred,
// so the pacer can mark the next output PCR with discontinuity_indicator.
func (c *sourceClock) TakeDiscontinuity() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := c.disc
	c.disc = false
	return d
}
