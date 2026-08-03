package mpegtsxc

import (
	"encoding/binary"
	"time"

	"go.uber.org/atomic"
)

// OutputDatagram is one complete output RTP datagram: 12-byte RTP header followed by
// whole 188-byte TS packets — ready to be TLV-framed as an rtp_ts part payload.
type OutputDatagram struct {
	Data          []byte
	RtpTs         uint32
	Seq           uint16
	Discontinuity bool // first datagram after an input discontinuity / grid rebase
}

// rtpPackager emits the merged packet stream as a CBR mux on a virtual output clock
// (parts mode). The output is a deterministic slot grid at StreamBitrate — outSTC(n)
// = anchor + n*tppTicks — where every slot holds a content packet, a synthesized PCR
// packet (fixed cadence, PCR = outSTC of its slot) or a null packet. No wall clock is
// involved: the grid advances as content arrives, so throughput is governed entirely
// by the pipeline. Slots are packed into RTP datagrams of DatagramPackets packets
// whose RTP timestamp is the outSTC of their first slot (90 kHz, wrapping mod 2^32).
type rtpPackager struct {
	cfg        *Config
	classifier *Classifier
	emit       func(OutputDatagram) error

	tppTicks        int64 // 27 MHz ticks per packet slot at StreamBitrate
	pcrIntervalPkts int64 // slots between synthesized PCR packets
	maxGapSlots     int64 // forward ets jump (in slots) that rebases the grid instead of padding

	anchor    int64 // outSTC(0), 27 MHz on the media timeline
	anchorSet bool
	sent      int64 // slots emitted
	lastPcrN  int64
	lastVidCC int

	// datagram assembly
	dg          []byte
	dgPkts      int
	dgFirstSlot int64
	dgDisc      bool
	seq         uint16
	ssrc        uint32
	pt          uint8

	// discPending is set from the Feed goroutine (input gap detector) and consumed on
	// the mux goroutine by the next synthesized PCR (discontinuity_indicator) or
	// datagram flush.
	discPending  atomic.Bool
	behindWarned bool

	outDatagrams atomic.Uint64
	null         [tsPacketSize]byte
}

func newRtpPackager(cfg *Config, classifier *Classifier, emit func(OutputDatagram) error) *rtpPackager {
	tpp := int64(tsPacketSize*8) * pcrClockHz / int64(cfg.StreamBitrate)
	if tpp < 1 {
		tpp = 1
	}
	pcrIntervalPkts := ticks27(cfg.PcrInterval) / tpp
	if pcrIntervalPkts < 1 {
		pcrIntervalPkts = 1
	}
	maxGapSlots := ticks27(time.Second) / tpp
	if maxGapSlots < 1 {
		maxGapSlots = 1
	}
	g := &rtpPackager{
		cfg:             cfg,
		classifier:      classifier,
		emit:            emit,
		tppTicks:        tpp,
		pcrIntervalPkts: pcrIntervalPkts,
		maxGapSlots:     maxGapSlots,
		lastPcrN:        -1 << 62,
		lastVidCC:       -1,
		ssrc:            cfg.SSRC,
		pt:              cfg.PayloadType,
		null:            nullPacket(),
	}
	return g
}

// SetInputRtpParams adopts the input stream's SSRC / payload type for fields the
// config leaves at zero. Called once, before the first output datagram.
func (g *rtpPackager) SetInputRtpParams(ssrc uint32, pt uint8) {
	if g.cfg.SSRC == 0 {
		g.ssrc = ssrc
	}
	if g.cfg.PayloadType == 0 {
		g.pt = pt
	}
}

// NoteDiscontinuity marks that an input discontinuity precedes the content not yet
// emitted. Safe to call from the Feed goroutine.
func (g *rtpPackager) NoteDiscontinuity() { g.discPending.Store(true) }

func (g *rtpPackager) OutDatagrams() uint64 { return g.outDatagrams.Load() }

// Packet places one merged content packet on the slot grid, filling any intervening
// slots with PCR/null packets.
func (g *rtpPackager) Packet(p mergedPkt) error {
	if !g.anchorSet {
		g.anchorSet = true
		g.anchor = p.ets
		log.Info("mpegts-xc packager anchored", "anchor", g.anchor,
			"tppTicks", g.tppTicks, "pcrIntervalPkts", g.pcrIntervalPkts)
	}

	nDue := g.sent
	if d := p.ets - g.anchor; d > 0 {
		nDue = d / g.tppTicks
	}
	switch {
	case nDue < g.sent:
		// Content behind the grid goes out in the next free slot. Persistent lateness
		// means the content rate exceeds StreamBitrate.
		if (g.sent-nDue)*g.tppTicks > int64(pcrClockHz) && !g.behindWarned {
			g.behindWarned = true
			log.Warn("mpegts-xc packager: content more than 1s behind the CBR grid — "+
				"StreamBitrate is too low for the content rate", "behindSlots", g.sent-nDue)
		}
		nDue = g.sent
	case nDue-g.sent > g.maxGapSlots:
		// Forward jump (source clock jump that slipped past the input gap detector):
		// rebase the grid instead of padding the gap with nulls.
		log.Info("mpegts-xc packager: forward timeline jump, rebasing grid",
			"gapSlots", nDue-g.sent)
		g.anchor = p.ets - g.sent*g.tppTicks
		g.discPending.Store(true)
		nDue = g.sent
	}

	for g.sent < nDue {
		if err := g.fillSlot(); err != nil {
			return err
		}
	}
	// The PCR cadence may be due at the content slot; the PCR packet takes the slot
	// first and the content shifts by one (as in the live CBR pacer).
	if g.pcrDue() {
		if err := g.pcrSlot(); err != nil {
			return err
		}
	}

	if p.isVideo && p.data.HasPayload() {
		g.lastVidCC = p.data.ContinuityCounter()
	}
	return g.slot(p.data[:])
}

// Finish flushes the final partial datagram. A still-pending discontinuity is
// surfaced on it (no more PCR packets are coming to carry the indicator).
func (g *rtpPackager) Finish() error {
	if g.discPending.Swap(false) {
		g.dgDisc = true
	}
	return g.flushDatagram()
}

// pcrSuppressed reports whether PCR synthesis is disabled: when the source carries
// PCR on a separate (passthrough) PID, those packets flow byte-exact and stay
// consistent with the media timeline — synthesize nothing.
func (g *rtpPackager) pcrSuppressed() bool {
	pcrPID := g.classifier.PcrPID()
	return pcrPID >= 0 && pcrPID != g.classifier.VideoPID()
}

func (g *rtpPackager) pcrDue() bool {
	if !g.anchorSet || g.classifier.VideoPID() < 0 || g.pcrSuppressed() {
		return false
	}
	return g.sent-g.lastPcrN >= g.pcrIntervalPkts
}

func (g *rtpPackager) pcrSlot() error {
	pcr := g.anchor + g.sent*g.tppTicks
	if pcr < 0 {
		pcr = 0
	}
	disc := g.discPending.Swap(false)
	if disc {
		g.dgDisc = true
	}
	pp := makePCRPacket(g.classifier.VideoPID(), uint64(pcr), g.lastVidCC, disc)
	g.lastPcrN = g.sent
	return g.slot(pp[:])
}

func (g *rtpPackager) fillSlot() error {
	if g.pcrDue() {
		return g.pcrSlot()
	}
	return g.slot(g.null[:])
}

// slot appends one TS packet to the current datagram and advances the grid.
func (g *rtpPackager) slot(pkt []byte) error {
	if g.dgPkts == 0 {
		g.dgFirstSlot = g.sent
		if g.dg == nil {
			g.dg = make([]byte, 12, 12+g.cfg.DatagramPackets*tsPacketSize)
		}
	}
	g.dg = append(g.dg, pkt...)
	g.dgPkts++
	g.sent++
	if g.dgPkts >= g.cfg.DatagramPackets {
		return g.flushDatagram()
	}
	return nil
}

func (g *rtpPackager) flushDatagram() error {
	if g.dgPkts == 0 {
		return nil
	}
	// If a discontinuity is pending but no synthesized PCR will ever carry the
	// indicator (separate PCR PID), surface it on the datagram instead. Otherwise
	// leave it pending for the next PCR packet.
	if g.pcrSuppressed() && g.discPending.Swap(false) {
		g.dgDisc = true
	}

	outStc := g.anchor + g.dgFirstSlot*g.tppTicks
	rtpTs := uint32(uint64(outStc/300) & 0xFFFFFFFF)

	hdr := g.dg[:12]
	hdr[0] = 0x80 // V=2, no padding, no extension, no CSRC
	hdr[1] = g.pt & 0x7F
	binary.BigEndian.PutUint16(hdr[2:4], g.seq)
	binary.BigEndian.PutUint32(hdr[4:8], rtpTs)
	binary.BigEndian.PutUint32(hdr[8:12], g.ssrc)

	out := OutputDatagram{
		Data:          g.dg,
		RtpTs:         rtpTs,
		Seq:           g.seq,
		Discontinuity: g.dgDisc,
	}

	g.seq++
	g.dg = nil
	g.dgPkts = 0
	g.dgDisc = false
	g.outDatagrams.Inc()

	return g.emit(out)
}
