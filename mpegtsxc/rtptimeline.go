package mpegtsxc

// rtpGapDetector flags input discontinuities from RTP header sequence/timestamp
// jumps (dropped datagrams, recorder gaps, source restarts). The RTP timestamps are
// not used for anything else: an RTP timestamp counter has an arbitrary offset from
// the stream's PCR/PES clock, so the merge timeline is derived from PCR/DTS instead
// (see mediaTimeline).
type rtpGapDetector struct {
	seqGapThreshold int64 // unwrapped seq jump that flags a discontinuity
	tsGapThreshold  int64 // RTP ts jump (90 kHz ticks) that flags a discontinuity

	initialized bool
	lastSeq     uint16
	lastTs      uint32

	discCount uint64
}

func newRtpGapDetector(seqGapThreshold int, tsGapThreshold90k int64) *rtpGapDetector {
	return &rtpGapDetector{
		seqGapThreshold: int64(seqGapThreshold),
		tsGapThreshold:  tsGapThreshold90k,
	}
}

// Update processes one input datagram's RTP header and reports whether an input
// discontinuity was detected at this datagram.
func (r *rtpGapDetector) Update(seq uint16, ts uint32) (discontinuity bool) {
	if !r.initialized {
		r.initialized = true
		r.lastSeq = seq
		r.lastTs = ts
		return false
	}

	// Signed deltas handle the seq (mod 2^16) and ts (mod 2^32) wraps.
	seqDelta := int64(int16(seq - r.lastSeq))
	tsDelta := int64(int32(ts - r.lastTs))
	r.lastSeq = seq
	r.lastTs = ts

	if seqDelta < 0 {
		seqDelta = -seqDelta
	}
	if tsDelta < 0 {
		tsDelta = -tsDelta
	}
	if seqDelta > r.seqGapThreshold || tsDelta > r.tsGapThreshold {
		r.discCount++
		log.Info("mpegts-xc: input discontinuity detected",
			"seqDelta", seqDelta, "tsDelta90k", tsDelta, "count", r.discCount)
		return true
	}
	return false
}

func (r *rtpGapDetector) Discontinuities() uint64 { return r.discCount }
