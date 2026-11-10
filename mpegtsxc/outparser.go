package mpegtsxc

import (
	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/pes"
	"github.com/Comcast/gots/v2/psi"
)

// avpipeOutParser parses the continuous MPEGTS muxed stream from avpipe_xc and
// post-processes it to get ready to interleave with the passthrough stream.
// - buffer to parse 188-aligned packets
// - detect video PID from PAT/PMT (ffmpeg mpegts muxer will make an arbitrary one)
// - re-map video packets to original PID
// - write continuity counter
// - regenerate PCR (or strip it when a pacer owns PCR)
//
// In parts mode (interpolate=true) packets are staged per access unit and released
// when the next AU's DTS is known, with each packet's emission timestamp (ets,
// 27 MHz) interpolated across the AU's frame interval — this spreads the AU's
// packets smoothly instead of a per-frame burst.
type avpipeOutParser struct {
	buf []byte // leftover bytes spanning Write calls

	outCh      chan<- videoPkt
	classifier *Classifier // source-side, for the target (source) video PID
	pcrLead    int64       // PCR lead before DTS, in 27 MHz ticks
	ccOut      uint8       // continuity counter on the remapped video PID
	stripPCR   bool        // strip PCR here because a pacer owns PCR (CBR / parts mode)

	pmtPID   int
	videoPID int
	patSeen  bool
	pmtSeen  bool

	auDTS int64 // DTS (90 kHz) of the access unit currently being emitted

	// AU staging (parts mode)
	timeline  *mediaTimeline // parts mode: DTS unwrapped onto the shared media timeline
	staged    []videoPkt     // packets of the AU currently being staged
	stagedDTS int64          // DTS of the staged AU (-1 = none)
	lastAuDur int64          // last observed AU duration (90 kHz), fallback for the final AU
}

// newAvpipeOutParser creates a parser. A non-nil timeline enables parts mode: AU
// staging with per-packet ets interpolation, DTS unwrapped onto the timeline.
func newAvpipeOutParser(outCh chan<- videoPkt, classifier *Classifier, pcrLead int64,
	stripPCR bool, timeline *mediaTimeline) *avpipeOutParser {
	return &avpipeOutParser{
		outCh: outCh, classifier: classifier, pcrLead: pcrLead, stripPCR: stripPCR,
		timeline: timeline,
		pmtPID:   -1, videoPID: -1, auDTS: -1, stagedDTS: -1,
	}
}

// Parse consumes a chunk of avpipe xc output buffering to obtain full TS packets
func (p *avpipeOutParser) Parse(chunk []byte) {
	p.buf = append(p.buf, chunk...)
	for len(p.buf) >= tsPacketSize {
		if p.buf[0] != 0x47 {
			// Resync to the next sync byte.
			i := 1
			for i < len(p.buf) && p.buf[i] != 0x47 {
				i++
			}
			p.buf = p.buf[i:]
			continue
		}
		var pkt packet.Packet
		copy(pkt[:], p.buf[:tsPacketSize])
		p.buf = p.buf[tsPacketSize:]
		p.handlePacket(pkt)
	}
}

// Flush releases the final staged access unit (parts mode). Call at stream EOF,
// before the output channel is closed.
func (p *avpipeOutParser) Flush() {
	if len(p.staged) == 0 {
		return
	}
	dur := p.lastAuDur
	if dur <= 0 {
		dur = 90 * 40 // 40 ms in 90 kHz ticks
	}
	p.releaseStaged(p.stagedDTS + dur)
}

func (p *avpipeOutParser) handlePacket(pkt packet.Packet) {
	switch pid := pkt.PID(); {
	case pid == 0x0000:
		p.parsePAT(pkt)
	case pid == p.pmtPID:
		p.parsePMT(pkt)
	case pid == p.videoPID:
		p.handleVideo(pkt)
	}
}

func (p *avpipeOutParser) parsePAT(pkt packet.Packet) {
	if p.patSeen || !pkt.PayloadUnitStartIndicator() {
		return
	}
	pl, err := pkt.Payload()
	if err != nil {
		return
	}
	pat, err := psi.NewPAT(pl)
	if err != nil {
		return
	}
	for program, pmtPID := range pat.ProgramMap() {
		if program != 0 {
			p.pmtPID = pmtPID
		}
	}
	p.patSeen = p.pmtPID >= 0
}

func (p *avpipeOutParser) parsePMT(pkt packet.Packet) {
	if p.pmtSeen || !pkt.PayloadUnitStartIndicator() {
		return
	}
	pl, err := pkt.Payload()
	if err != nil {
		return
	}
	pmt, err := psi.NewPMT(pl)
	if err != nil {
		return
	}
	for _, es := range pmt.ElementaryStreams() {
		if es.IsVideoContent() {
			p.videoPID = es.ElementaryPid()
			break
		}
	}
	p.pmtSeen = true
	log.Info("avpipe-out: video PID resolved",
		"avpipeVideoPID", p.videoPID, "remapTo", p.classifier.VideoPID())
}

func (p *avpipeOutParser) handleVideo(pkt packet.Packet) {
	if pkt.PayloadUnitStartIndicator() {
		newDTS := int64(-1)
		if pl, err := pkt.Payload(); err == nil {
			if ph, err := pes.NewPESHeader(pl); err == nil {
				if ph.HasDTS() {
					newDTS = int64(ph.DTS())
				} else if ph.HasPTS() {
					newDTS = int64(ph.PTS())
				}
			}
		}
		if p.timeline != nil && newDTS >= 0 {
			newDTS = p.timeline.unwrap(newDTS)
			if len(p.staged) > 0 {
				// The next AU's DTS bounds the staged AU's frame interval.
				p.releaseStaged(newDTS)
			}
		}
		p.auDTS = newDTS
	}
	p.emitRemapped(pkt)
}

// emitRemapped copies the avpipe video packet into the output channel (or the AU
// stage in parts mode)
// - rewrite PID and continuity counter
// - PCR regenerated on the source clock or stripped when a pacer owns PCR
func (p *avpipeOutParser) emitRemapped(pkt packet.Packet) {
	if p.outCh == nil || p.auDTS < 0 {
		return
	}
	vpid := p.classifier.VideoPID()
	if vpid < 0 {
		return
	}

	out := pkt // value copy ([188]byte)
	out.SetPID(vpid)
	out.SetContinuityCounter(int(p.ccOut))
	if out.HasPayload() {
		p.ccOut = (p.ccOut + 1) & 0x0f
	}

	// PCR handling
	// - when a pacer owns PCR (CBR / parts mode), strip PCR here
	// - otherwise regenerate it on the source clock based on DTS
	if out.HasAdaptationField() {
		if af, err := out.AdaptationField(); err == nil {
			if has, _ := af.HasPCR(); has {
				if p.stripPCR {
					_ = af.SetHasPCR(false)
				} else {
					pcr := p.auDTS*300 - p.pcrLead
					if pcr < 0 {
						pcr = 0
					}
					_ = af.SetPCR(uint64(pcr))
				}
			}
		}
	}

	if p.timeline != nil {
		p.staged = append(p.staged, videoPkt{data: out, dts: p.auDTS})
		p.stagedDTS = p.auDTS
		return
	}
	p.outCh <- videoPkt{data: out, dts: p.auDTS}
}

// releaseStaged emits the staged AU's packets with ets interpolated between this
// AU's emission start and the next AU's (delivery leads decode by pcrLead).
func (p *avpipeOutParser) releaseStaged(nextDTS int64) {
	n := int64(len(p.staged))
	if n == 0 {
		return
	}
	dur := nextDTS - p.stagedDTS
	if dur <= 0 {
		dur = p.lastAuDur
		if dur <= 0 {
			dur = 90 * 40 // 40 ms in 90 kHz ticks
		}
	} else {
		p.lastAuDur = dur
	}
	start := p.stagedDTS*300 - p.pcrLead
	span := dur * 300
	for k := range p.staged {
		vp := p.staged[k]
		vp.ets = start + int64(k)*span/n
		p.outCh <- vp
	}
	p.staged = p.staged[:0]
	p.stagedDTS = -1
}
