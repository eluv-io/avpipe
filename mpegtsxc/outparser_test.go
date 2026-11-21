package mpegtsxc

import (
	"testing"

	"github.com/Comcast/gots/v2/packet"
)

// encodePesTs encodes a 33-bit 90 kHz timestamp into the 5-byte PES form.
func encodePesTs(prefix byte, ts int64) [5]byte {
	var b [5]byte
	b[0] = prefix | byte((ts>>29)&0x0e) | 1
	b[1] = byte(ts >> 22)
	b[2] = byte((ts>>14)&0xfe) | 1
	b[3] = byte(ts >> 7)
	b[4] = byte((ts<<1)&0xfe) | 1
	return b
}

// pesStartPacket builds a TS packet with PUSI set whose payload begins a video PES
// packet carrying the given PTS and DTS.
func pesStartPacket(pid int, cc int, pts, dts int64) packet.Packet {
	var p packet.Packet
	for i := range p {
		p[i] = 0xff
	}
	p[0] = 0x47
	p[1] = 0x40 | byte((pid>>8)&0x1f) // PUSI
	p[2] = byte(pid & 0xff)
	p[3] = 0x10 | byte(cc&0x0f) // payload only

	pes := p[4:]
	pes[0], pes[1], pes[2] = 0x00, 0x00, 0x01
	pes[3] = 0xe0                  // video stream id
	pes[4], pes[5] = 0x00, 0x00    // PES packet length 0 (video)
	pes[6] = 0x80                  // marker bits
	pes[7] = 0xc0                  // PTS+DTS flags
	pes[8] = 10                    // header data length
	ptsB := encodePesTs(0x30, pts) // '0011' for PTS in PTS+DTS
	dtsB := encodePesTs(0x10, dts) // '0001' for DTS
	copy(pes[9:14], ptsB[:])
	copy(pes[14:19], dtsB[:])
	return p
}

// contPacket builds a payload-continuation TS packet on the PID (no PUSI).
func contPacket(pid int, cc int) packet.Packet {
	return tsPkt(pid, cc)
}

func TestOutparserAuStagingInterpolation(t *testing.T) {
	const avpipePID = 0x100       // PID assigned by ffmpeg's muxer
	const srcPID = 0x1e1          // source video PID to remap onto
	pcrLead := int64(300 * 27000) // 300 ms in 27 MHz ticks

	outCh := make(chan videoPkt, 64)
	cls := testClassifier(srcPID, srcPID)
	p := newAvpipeOutParser(outCh, cls, pcrLead, true, &mediaTimeline{})
	// PAT/PMT parsing is exercised end-to-end; pin the resolved video PID here.
	p.patSeen, p.pmtSeen = true, true
	p.pmtPID = 0x20
	p.videoPID = avpipePID

	const dts0 = int64(900000) // 10s
	const dur = int64(3600)    // 40 ms per AU
	// AU 0: PUSI + 3 continuation packets; AU 1: PUSI + 1 continuation.
	pkts := []packet.Packet{
		pesStartPacket(avpipePID, 0, dts0+dur, dts0),
		contPacket(avpipePID, 1),
		contPacket(avpipePID, 2),
		contPacket(avpipePID, 3),
		pesStartPacket(avpipePID, 4, dts0+2*dur, dts0+dur),
		contPacket(avpipePID, 5),
	}
	for _, pkt := range pkts {
		p.Parse(pkt[:])
	}

	// AU 0 (4 packets) is released when AU 1's PUSI arrives.
	if len(outCh) != 4 {
		t.Fatalf("released %d packets, want 4", len(outCh))
	}
	start0 := dts0*300 - pcrLead
	start1 := (dts0+dur)*300 - pcrLead
	span := start1 - start0
	for k := 0; k < 4; k++ {
		vp := <-outCh
		wantEts := start0 + int64(k)*span/4
		if vp.ets != wantEts {
			t.Fatalf("packet %d ets = %d, want %d", k, vp.ets, wantEts)
		}
		if vp.dts != dts0 {
			t.Fatalf("packet %d dts = %d, want %d", k, vp.dts, dts0)
		}
		if vp.data.PID() != srcPID {
			t.Fatalf("packet %d not remapped: PID %#x", k, vp.data.PID())
		}
		if cc := vp.data.ContinuityCounter(); cc != k&0x0f {
			t.Fatalf("packet %d CC = %d, want %d", k, cc, k)
		}
	}

	// Flush releases AU 1 using the last AU duration as fallback.
	p.Flush()
	if len(outCh) != 2 {
		t.Fatalf("flush released %d packets, want 2", len(outCh))
	}
	startA1 := (dts0+dur)*300 - pcrLead
	spanA1 := dur * 300
	for k := 0; k < 2; k++ {
		vp := <-outCh
		wantEts := startA1 + int64(k)*spanA1/2
		if vp.ets != wantEts {
			t.Fatalf("flush packet %d ets = %d, want %d", k, vp.ets, wantEts)
		}
	}
}

func TestOutparserLiveModeNoStaging(t *testing.T) {
	const avpipePID = 0x100
	const srcPID = 0x1e1

	outCh := make(chan videoPkt, 16)
	cls := testClassifier(srcPID, srcPID)
	p := newAvpipeOutParser(outCh, cls, 300*27000, false, nil) // live mode: no timeline
	p.patSeen, p.pmtSeen = true, true
	p.pmtPID = 0x20
	p.videoPID = avpipePID

	pkt := pesStartPacket(avpipePID, 0, 900000+3600, 900000)
	p.Parse(pkt[:])

	// Live mode emits immediately, no staging.
	if len(outCh) != 1 {
		t.Fatalf("emitted %d packets, want 1", len(outCh))
	}
	vp := <-outCh
	if vp.dts != 900000 {
		t.Fatalf("dts = %d", vp.dts)
	}
}
