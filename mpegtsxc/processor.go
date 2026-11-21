package mpegtsxc

import (
	"fmt"
	"time"

	"github.com/Comcast/gots/v2/packet"
	"github.com/eluv-io/avpipe/broadcastproto/mpegts"
)

// processor routes each TS packet read from the source:
//   - video PID + PAT/PMT  -> forwarded to avpipe xc
//   - PAT/PMT + everything else (audio, data, PCR-PID, null) -> passthrough FIFO
//
// It also tracks the most recent PCR (27 MHz, from the PCR PID) used to tag FIFO
// items, and tracks per-stream PES PTS for interleave stats.
//
// In parts mode (timeline != nil) the PCR tag is unwrapped onto the shared media
// timeline (comparable with the video leg's unwrapped DTS for the exact merge),
// FIFO pushes block for backpressure, and packets are discarded until the first PCR
// establishes the clock.
type processor struct {
	classifier *Classifier
	selector   *mpegts.Selector
	fifo       *PassthroughFifo
	stats      *Stats
	srcClock   *sourceClock   // optional (live CBR mode); fed input PCR for phase-lock
	timeline   *mediaTimeline // parts mode: shared PCR/DTS unwrapper

	currentPCR uint64 // most recent PCR (27 MHz); 0 until the first PCR is seen
	currentTag int64  // parts mode: unwrapped PCR tag (27 MHz on the media timeline)
	clockSeen  bool   // parts mode: first PCR observed
}

func newProcessor(c *Classifier, selector *mpegts.Selector, f *PassthroughFifo, s *Stats, srcClock *sourceClock, timeline *mediaTimeline) *processor {
	return &processor{classifier: c, selector: selector, fifo: f, stats: s, srcClock: srcClock, timeline: timeline}
}

type dgCounts struct{ video, other, psi uint64 }

// handleDatagram
// - splits one datagram of raw TS into 188-byte TS packets
// - classifies
// - pushes passthrough packets into the FIFO (records the counts in stats)
// - returns a freshly-allocated buffer of the video + PSI packets to forward to avpipe xc
func (p *processor) handleDatagram(data []byte) (forward []byte, retErr error) {
	var counts dgCounts
	defer func() { p.stats.addDatagram(counts) }()

	if len(data) < tsPacketSize || data[0] != 0x47 {
		// Raw TS expected: sync byte at offset 0 (RTP is stripped upstream).
		return nil, nil
	}

	forward = make([]byte, 0, len(data))
	for off := 0; off+tsPacketSize <= len(data); off += tsPacketSize {
		var pkt packet.Packet
		copy(pkt[:], data[off:off+tsPacketSize])

		selected := []packet.Packet{pkt}
		if p.selector != nil {
			var err error
			selected, err = p.selector.Push(&pkt)
			if err != nil {
				return forward, fmt.Errorf("mpegtsxc: source selection failed: %w", err)
			}
			if err := applyResolvedSelection(p.classifier, p.selector.Snapshot()); err != nil {
				return forward, err
			}
		}

		for i := range selected {
			p.handlePacket(selected[i], &counts, &forward)
		}
	}
	return forward, nil
}

func applyResolvedSelection(classifier *Classifier, snapshot mpegts.SelectionSnapshot) error {
	if !snapshot.Ready {
		return nil
	}
	if len(snapshot.ProgramIDs) != 1 {
		return fmt.Errorf("mpegtsxc: selection resolved to %d programs; exactly one is required", len(snapshot.ProgramIDs))
	}
	if len(snapshot.VideoPIDs) != 1 {
		return fmt.Errorf("mpegtsxc: selection resolved to %d explicitly selected video PIDs; exactly one is required", len(snapshot.VideoPIDs))
	}
	if len(snapshot.PCRPIDs) != 1 {
		return fmt.Errorf("mpegtsxc: selection resolved to %d PCR PIDs; exactly one is required", len(snapshot.PCRPIDs))
	}
	classifier.SetSelection(snapshot.PMTPIDs, snapshot.VideoPIDs[0], snapshot.PCRPIDs[0])
	return nil
}

func (p *processor) handlePacket(pkt packet.Packet, counts *dgCounts, forward *[]byte) {
	class := p.classifier.Classify(pkt)

	// Discard everything until the video PID is known (PMT parsed).
	if !p.classifier.Ready() {
		return
	}

	p.updatePCR(pkt)
	p.trackPTS(pkt, class)

	parts := p.timeline != nil
	if parts && !p.clockSeen {
		// Parts mode: no merge tag exists until the first PCR; discard the preroll
		// (PAT/PMT repeat continuously, so nothing essential is lost).
		return
	}
	tag := int64(p.currentPCR)
	if parts {
		tag = p.currentTag
	}

	// Forward only the video PID + PAT/PMT to avpipe xc
	// Only works when PCR is in the video PID (which is common).
	// TODO: if the PCR PID differs from the video PID, must forward the PCR-PID packets
	// avpipe xc so it has a clock reference (or else likely makes garbage timestamps)
	switch class {
	case ClassVideo:
		counts.video++
		*forward = append(*forward, pkt[:]...)
	case ClassPSI:
		counts.psi++
		*forward = append(*forward, pkt[:]...)
		p.push(tsItem{data: pkt, ets: tag}, parts)
	default:
		counts.other++
		p.push(tsItem{data: pkt, ets: tag}, parts)
	}
}

func (p *processor) push(item tsItem, blocking bool) {
	if blocking {
		p.fifo.PushWait(item)
	} else {
		p.fifo.Push(item)
	}
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
		if p.timeline != nil {
			p.currentTag = p.timeline.unwrap(int64(pcr/300))*300 + int64(pcr%300)
			p.clockSeen = true
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
