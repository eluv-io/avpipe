package mpegtsxc

import "sync"

// mediaTimeline unwraps 33-bit 90 kHz clock values (PCR base, PTS/DTS) onto a single
// continuous int64 timeline. It is shared between the input side (PCR tags on
// passthrough packets) and the video transcode leg (PES DTS), which lags the input by
// at most the encoder latency — far less than the 13h nearest-wrap ambiguity window —
// so mapping each value to the representation nearest the last one keeps both sides
// on the same line.
//
// Note: input RTP timestamps are NOT on this timeline — an RTP timestamp counter has
// an arbitrary offset from the PCR/PES counter — which is why the merge timeline is
// derived from PCR/DTS and RTP headers are only used for gap detection.
type mediaTimeline struct {
	mu          sync.Mutex
	initialized bool
	ref         int64 // last unwrapped value (90 kHz)
}

// unwrap maps a 33-bit 90 kHz clock value onto the continuous timeline, choosing the
// representation nearest the previously unwrapped value.
func (m *mediaTimeline) unwrap(v int64) int64 {
	const mod = int64(1) << 33

	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.initialized {
		m.initialized = true
		m.ref = v
		return v
	}
	d := (v - m.ref) % mod
	if d < 0 {
		d += mod
	}
	if d >= mod/2 {
		d -= mod
	}
	m.ref += d
	return m.ref
}
