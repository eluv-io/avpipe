package mpegtsxc

import (
	gots "github.com/Comcast/gots/v2"
	"github.com/Comcast/gots/v2/packet"
)

// nullPacket returns a TS null packet (PID 0x1FFF, payload-only stuffing).
func nullPacket() packet.Packet {
	var p packet.Packet
	for i := range p {
		p[i] = 0xff
	}
	p[0] = 0x47
	p[1] = 0x1f // PID 0x1FFF high bits, PUSI=0
	p[2] = 0xff // PID low bits
	p[3] = 0x10 // adaptation_field_control=01 (payload only), CC=0
	return p
}

// makePCRPacket returns an adaptation-only TS packet (no payload) on PCR PID.
// - cc is the last continuity counter seen on the video PID (adaptation-only packets do not increment CC).
func makePCRPacket(pid int, pcr uint64, cc int, disc bool) packet.Packet {
	var p packet.Packet
	for i := range p {
		p[i] = 0xff
	}
	ccBits := 0
	if cc >= 0 {
		ccBits = cc & 0x0f
	}
	flags := byte(0x10) // PCR_flag
	if disc {
		flags |= 0x80 // discontinuity_indicator
	}
	p[0] = 0x47
	p[1] = byte((pid >> 8) & 0x1f) // PUSI=0
	p[2] = byte(pid & 0xff)
	p[3] = 0x20 | byte(ccBits) // adaptation_field_control=10 (adaptation only)
	p[4] = 183                 // adaptation_field_length (fills the rest of the packet)
	p[5] = flags
	gots.InsertPCR(p[6:12], pcr)
	return p
}
