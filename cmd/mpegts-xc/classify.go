package main

import (
	"fmt"

	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/psi"
)

// PacketClass is the basic packet classifier into video and 'other'
type PacketClass int

const (
	// ClassOther is any packet we pass through untouched (audio, data, PCR-only ...)
	ClassOther PacketClass = iota

	// ClassPSI is a PAT/PMT packet - must be forwared both to avpipe xc and the 'other' queue
	ClassPSI

	// ClassVideo is a packet on the video PID - forwarded to avpipe xc
	ClassVideo
)

// Classifier discovers the video PID from PAT/PMT and labels each TS packet
type Classifier struct {
	patSeen  bool
	pmtPIDs  map[int]bool // PMT PIDs learned from the PAT
	videoPID int
	pcrPID   int
	// videoType is the PMT stream_type of the video ES (e.g. 0x1b H.264, 0x24 HEVC)
	videoType uint8
}

func NewClassifier() *Classifier {
	return &Classifier{pmtPIDs: map[int]bool{}, videoPID: -1, pcrPID: -1}
}

func (c *Classifier) VideoPID() int { return c.videoPID }
func (c *Classifier) PcrPID() int   { return c.pcrPID }
func (c *Classifier) Ready() bool   { return c.videoPID >= 0 }

// Classify inspects a 188-byte TS packet and returns its 'class'
// Updates PAT/PMT state
func (c *Classifier) Classify(pkt packet.Packet) PacketClass {
	pid := pkt.PID()

	// PAT (PID 0x0000): learn every program's PMT PID.
	if pid == 0x0000 {
		if !c.patSeen && pkt.PayloadUnitStartIndicator() {
			c.parsePAT(pkt)
		}
		return ClassPSI
	}

	// PMT: resolve the video PID and its stream type.
	if c.pmtPIDs[pid] {
		if c.videoPID == -1 && pkt.PayloadUnitStartIndicator() {
			c.parsePMT(pkt)
		}
		return ClassPSI
	}

	if c.videoPID >= 0 && pid == c.videoPID {
		return ClassVideo
	}

	return ClassOther
}

func (c *Classifier) parsePAT(pkt packet.Packet) {
	payload, err := pkt.Payload()
	if err != nil {
		log.Warn("classifier: PAT payload error", "err", err)
		return
	}
	pat, err := psi.NewPAT(payload)
	if err != nil {
		log.Warn("classifier: PAT parse error", "err", err)
		return
	}
	for program, pmtPID := range pat.ProgramMap() {
		if program == 0 {
			continue // program 0 is the network PID (NIT), not a PMT
		}
		c.pmtPIDs[pmtPID] = true
		log.Info("classifier: PAT program", "program", program, "pmtPID", pmtPID)
	}
	if len(c.pmtPIDs) > 0 {
		c.patSeen = true
	} else {
		log.Warn("classifier: PAT had no programs")
	}
}

func (c *Classifier) parsePMT(pkt packet.Packet) {
	payload, err := pkt.Payload()
	if err != nil {
		log.Warn("classifier: PMT payload error", "err", err)
		return
	}
	pmt, err := psi.NewPMT(payload)
	if err != nil {
		log.Warn("classifier: PMT parse error", "err", err)
		return
	}

	pcrPID := pcrPIDFromPMTPayload(payload)
	c.pcrPID = pcrPID
	log.Info("classifier: PMT parsed",
		"pmtPID", pkt.PID(), "pcrPID", pcrPID, "version", pmt.VersionNumber())

	for _, es := range pmt.ElementaryStreams() {
		log.Info("classifier: PMT stream",
			"pid", es.ElementaryPid(),
			"streamType", fmt.Sprintf("0x%02x", es.StreamType()),
			"desc", es.StreamTypeDescription())
		for _, d := range es.Descriptors() {
			log.Info("classifier: PMT descriptor",
				"pid", es.ElementaryPid(), "tag", fmt.Sprintf("0x%02x", d.Tag()), "fmt", d.Format())
		}
		if c.videoPID == -1 && es.IsVideoContent() {
			c.videoPID = es.ElementaryPid()
			c.videoType = es.StreamType()
		}
	}

	if c.videoPID >= 0 {
		log.Info("classifier: video PID resolved",
			"videoPID", c.videoPID, "streamType", fmt.Sprintf("0x%02x", c.videoType),
			"pcrOnVideoPID", pcrPID == c.videoPID)
	} else {
		log.Warn("classifier: PMT had no video content stream")
	}
}

// pcrPIDFromPMTPayload extracts the 13-bit PCR_PID from a PMT section payload.
// (gots doesn't expose it - switch to gots when it does)
// Returns -1 if the payload is too short.
func pcrPIDFromPMTPayload(payload []byte) int {
	if len(payload) < 1 {
		return -1
	}
	sec := 1 + int(payload[0]) // skip pointer_field
	if len(payload) < sec+10 {
		return -1
	}
	return (int(payload[sec+8]&0x1f) << 8) | int(payload[sec+9])
}
