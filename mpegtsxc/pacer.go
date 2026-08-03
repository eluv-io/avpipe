package mpegtsxc

import (
	"io"
	"time"

	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/pes"
)

// pcrOutIntervalMs is the maximum spacing between PCRs in the *output* stream (DVB limit is 40ms)
const pcrOutIntervalMs = 35

// pacer wraps the output sink and emits a constant-bitrate stream:
//   - exactly one TS packet per slot at the target rate
//   - fill slots that have no content with TS null packets (PID 0x1FFF).
//   - insert PCR on the video PID at a fixed output cadence with a value derived
//     from the output packet index (base/anchor + n*ticks_per_packet)
//
// Implements io.WriteCloser - upstream muxer writes content packets via Write
type pacer struct {
	sink       io.WriteCloser
	ratebps    int
	classifier *Classifier
	srcClock   *sourceClock // optional; when locked, the output is phase-locked to it
	pcrLead    int64        // PCR lead before DTS, in 27 MHz ticks
	in         chan packet.Packet
	done       chan struct{}
}

func newPacer(sink io.WriteCloser, ratebps int, classifier *Classifier, srcClock *sourceClock, pcrLead int64) *pacer {
	p := &pacer{
		sink: sink, ratebps: ratebps, classifier: classifier, srcClock: srcClock, pcrLead: pcrLead,
		in:   make(chan packet.Packet, 8192),
		done: make(chan struct{}),
	}
	go p.run()
	return p
}

func (p *pacer) Write(b []byte) (int, error) {
	var pkt packet.Packet
	copy(pkt[:], b)
	p.in <- pkt // backpressure: blocks if the pacer is behind
	return len(b), nil
}

func (p *pacer) Close() error {
	close(p.in)
	<-p.done
	return p.sink.Close()
}

func (p *pacer) run() {
	defer close(p.done)

	R := int64(p.ratebps)
	tppTicks := int64(tsPacketSize*8) * pcrClockHz / R // 27 MHz ticks per packet
	pcrIntervalPkts := int64(pcrOutIntervalMs) * R / 1000 / int64(tsPacketSize*8)
	if pcrIntervalPkts < 1 {
		pcrIntervalPkts = 1
	}

	var (
		sent      int64 // output packet index
		lastPcrN  int64 = -1 << 62
		anchorPCR int64 = -1 // 27 MHz; set from first video AU's DTS/PTS
		anchorN   int64
		lastVidCC = -1
		null      = nullPacket()
		start     time.Time
		started   bool

		// phase-lock anchoring (set when the source clock first reports locked)
		pllAnchored bool
		pllAnchorN  int64
		pllAnchorST int64
	)

	handleContent := func(c packet.Packet) {
		if vpid := p.classifier.VideoPID(); vpid >= 0 && c.PID() == vpid {
			if c.HasPayload() {
				lastVidCC = c.ContinuityCounter()
			}
			if anchorPCR < 0 && c.PayloadUnitStartIndicator() {
				if pl, err := c.Payload(); err == nil {
					if ph, err := pes.NewPESHeader(pl); err == nil {
						base := int64(-1)
						if ph.HasDTS() {
							base = int64(ph.DTS())
						} else if ph.HasPTS() {
							base = int64(ph.PTS())
						}
						if base >= 0 {
							anchorPCR = base*300 - p.pcrLead
							anchorN = sent
						}
					}
				}
			}
		}
		p.sink.Write(c[:])
		sent++
	}

	// emitOne fills the current slot.
	// Return false once content channel is closed and drained.
	emitOne := func() bool {
		vpid := p.classifier.VideoPID()
		if anchorPCR >= 0 && vpid >= 0 && sent-lastPcrN >= pcrIntervalPkts {
			pcr := anchorPCR + (sent-anchorN)*tppTicks
			disc := p.srcClock != nil && p.srcClock.TakeDiscontinuity()
			pp := makePCRPacket(vpid, uint64(pcr), lastVidCC, disc)
			p.sink.Write(pp[:])
			lastPcrN = sent
			sent++
			return true
		}
		select {
		case c, ok := <-p.in:
			if !ok {
				return false
			}
			handleContent(c)
		default:
			p.sink.Write(null[:])
			sent++
		}
		return true
	}

	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		if !started {
			// Anchor the output clock to the first content packet
			select {
			case c, ok := <-p.in:
				if !ok {
					return
				}
				start = time.Now()
				started = true
				handleContent(c)
			default:
			}
			continue
		}
		// Leaky bucket: emit as many packets as the clock allows.
		// When the source clock is locked - use it (or else run on local clock)
		now := time.Now()
		var target int64
		if p.srcClock != nil {
			if stc, ok := p.srcClock.Now(now); ok {
				if !pllAnchored {
					pllAnchored = true
					pllAnchorN = sent
					pllAnchorST = stc
					log.Info("mpegts-xc pacer phase-locked to source clock", "atPacket", sent)
				}
				target = pllAnchorN + (stc-pllAnchorST)/tppTicks
			} else {
				pllAnchored = false // (re)anchor when the loop (re)locks
			}
		}
		if !pllAnchored {
			target = int64(now.Sub(start).Seconds() * float64(R) / float64(tsPacketSize*8))
		}
		for sent < target {
			if !emitOne() {
				return
			}
		}
	}
}
