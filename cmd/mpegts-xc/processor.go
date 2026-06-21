package main

import (
	"time"

	"github.com/Comcast/gots/v2/packet"
)

// processor routes each TS packet read from the source:
//   - video PID + PAT/PMT  -> forwarded to avpipe xc
//   - PAT/PMT + everything else (audio, data, PCR-PID, null) -> passthrough FIFO
//
// It also tracks the most recent PCR (27 MHz, from the PCR PID) used to tag FIFO
// items, and tracks per-stream PES PTS for interleave stats.
type processor struct {
	classifier *Classifier
	fifo       *PassthroughFifo
	stats      *Stats
	srcClock   *sourceClock // optional; fed input PCR for phase-lock

	currentPCR uint64 // most recent PCR (27 MHz); 0 until the first PCR is seen
}

func newProcessor(c *Classifier, f *PassthroughFifo, s *Stats, srcClock *sourceClock) *processor {
	return &processor{classifier: c, fifo: f, stats: s, srcClock: srcClock}
}

type dgCounts struct{ video, other, psi uint64 }

// handleDatagram
// - splits one UDP datagram into 188-byte TS packets
// - classifies
// - pushes passthrough packets into the FIFO (records the counts in stats)
// - returns a freshly-allocated buffer of the video + PSI packets to forward to avpipe xc
func (p *processor) handleDatagram(data []byte) (forward []byte) {
	var counts dgCounts
	defer func() { p.stats.addDatagram(counts) }()

	if len(data) < tsPacketSize || data[0] != 0x47 {
		// Raw UDP/TS expected: sync byte at offset 0 (RTP is stripped upstream).
		return nil
	}

	forward = make([]byte, 0, len(data))
	for off := 0; off+tsPacketSize <= len(data); off += tsPacketSize {
		var pkt packet.Packet
		copy(pkt[:], data[off:off+tsPacketSize])

		class := p.classifier.Classify(pkt)

		// Discard everything until the video PID is known (PMT parsed).
		if !p.classifier.Ready() {
			continue
		}

		p.updatePCR(pkt)
		p.trackPTS(pkt, class)

		// Forward only the video PID + PAT/PMT to avpipe xc
		// Only works when PCR is in the video PID (which is common).
		// TODO: if the PCR PID differs from the video PID, must forward the PCR-PID packets
		// avpipe xc so it has a clock reference (or else likely makes garbage timestamps)
		switch class {
		case ClassVideo:
			counts.video++
			forward = append(forward, pkt[:]...)
		case ClassPSI:
			counts.psi++
			forward = append(forward, pkt[:]...)
			p.fifo.Push(tsItem{data: pkt, pcr: p.currentPCR})
		default:
			counts.other++
			p.fifo.Push(tsItem{data: pkt, pcr: p.currentPCR})
		}
	}
	return forward
}

// updatePCR advances the clock from PCR samples on the PCR PID.
func (p *processor) updatePCR(pkt packet.Packet) {
	if pkt.PID() != p.classifier.PcrPID() || !pkt.HasAdaptationField() {
		return
	}
	af, err := pkt.AdaptationField()
	if err != nil || af == nil {
		return
	}
	if has, err := af.HasPCR(); err != nil || !has {
		return
	}
	if pcr, err := af.PCR(); err == nil {
		p.currentPCR = pcr
		if p.srcClock != nil {
			p.srcClock.Update(pcr, time.Now())
		}
	}
}

// trackPTS records the PES PTS of video and "other" streams for interleave stats.
func (p *processor) trackPTS(pkt packet.Packet, class PacketClass) {
	// Wait until the video PID is resolved (PMT parsed)
	if !p.classifier.Ready() {
		return
	}
	if !pkt.PayloadUnitStartIndicator() || class == ClassPSI {
		return
	}
	payload, err := pkt.Payload()
	if err != nil {
		return
	}
	pts, ok := pesPTS(payload)
	if !ok {
		return
	}
	if class == ClassVideo {
		p.stats.setVideoPTS(pts)
	} else {
		p.stats.setOtherPTS(pkt.PID(), pts)
	}
}
