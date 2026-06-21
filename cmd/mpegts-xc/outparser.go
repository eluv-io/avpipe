package main

import (
	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/pes"
	"github.com/Comcast/gots/v2/psi"
)

// avpipeOutParser parses the continuous MPEGTS muxed stream from avpipe_xc and
// post-processes it to get ready to interleve with the passhtrough stream.
// - buffer to parse 188-aligned packets
// - detect video PID from PAT/PMT (ffmpeg mpegts muxer will make an arbitrary one)
// - re-map video packets to original PID
// - write continuity counter
// - regenerate PCR
type avpipeOutParser struct {
	buf []byte // leftover bytes spanning Write calls

	outCh      chan<- videoPkt
	classifier *Classifier // source-side, for the target (source) video PID
	pcrLead    int64       // PCR lead before DTS, in 27 MHz ticks
	ccOut      uint8       // continuity counter on the remapped video PID
	cbr        bool        // CBR mode: strip PCR here because pacer owns it in CBR mode

	pmtPID   int
	videoPID int
	patSeen  bool
	pmtSeen  bool

	auDTS int64 // DTS (90 kHz) of the access unit currently being emitted
}

// newAvpipeOutParser creates a parser
func newAvpipeOutParser(outCh chan<- videoPkt, classifier *Classifier, pcrLead int64, cbr bool) *avpipeOutParser {
	return &avpipeOutParser{
		outCh: outCh, classifier: classifier, pcrLead: pcrLead, cbr: cbr,
		pmtPID: -1, videoPID: -1, auDTS: -1,
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
		p.auDTS = -1
		if pl, err := pkt.Payload(); err == nil {
			if ph, err := pes.NewPESHeader(pl); err == nil {
				if ph.HasDTS() {
					p.auDTS = int64(ph.DTS())
				} else if ph.HasPTS() {
					p.auDTS = int64(ph.PTS())
				}
			}
		}
	}
	p.emitRemapped(pkt)
}

// emitRemapped copies the avpipe video packet into the output channel
// - rewrite PID and continuity counter
// - PCR regenerated on the source clock or stripped in CBR mode
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
	// - in CBR mode the pacer owns PCR so strip PCR here
	// - otherwise regenerate it on the source clock based on DTS
	if out.HasAdaptationField() {
		if af, err := out.AdaptationField(); err == nil {
			if has, _ := af.HasPCR(); has {
				if p.cbr {
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

	p.outCh <- videoPkt{data: out, dts: p.auDTS}
}
