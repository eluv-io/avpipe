package mpegtsxc

import (
	"testing"
	"time"

	"github.com/Comcast/gots/v2/packet"
)

// packagerConfig: StreamBitrate 1,504,000 bps => exactly 27000 ticks (1 ms) per slot;
// PcrInterval 35 ms => 35 slots between PCR packets.
func packagerConfig() Config {
	return Config{
		StreamBitrate:   1_504_000,
		VideoBitrate:    1_000_000,
		DatagramPackets: 7,
		PcrInterval:     35 * time.Millisecond,
		PcrLead:         300 * time.Millisecond,
		SeqGapThreshold: 64,
		TsGapThreshold:  time.Second,
		PayloadType:     33,
		SSRC:            0xabcd1234,
	}.withDefaults()
}

func testClassifier(videoPID, pcrPID int) *Classifier {
	c := NewClassifier()
	c.videoPID = videoPID
	c.pcrPID = pcrPID
	return c
}

func collectPackager(t *testing.T, cls *Classifier) (*rtpPackager, *[]OutputDatagram) {
	t.Helper()
	var out []OutputDatagram
	cfg := packagerConfig()
	g := newRtpPackager(&cfg, cls, func(d OutputDatagram) error {
		out = append(out, d)
		return nil
	})
	return g, &out
}

func datagramPackets(t *testing.T, d OutputDatagram) []packet.Packet {
	t.Helper()
	if (len(d.Data)-12)%tsPacketSize != 0 {
		t.Fatalf("datagram payload not 188-aligned: %d", len(d.Data)-12)
	}
	var pkts []packet.Packet
	for off := 12; off < len(d.Data); off += tsPacketSize {
		var p packet.Packet
		copy(p[:], d.Data[off:off+tsPacketSize])
		pkts = append(pkts, p)
	}
	return pkts
}

func TestPackagerSlotGridAndDatagrams(t *testing.T) {
	g, out := collectPackager(t, testClassifier(0x1e1, 0x1e1))

	const anchor = int64(27_000_000) // 1s on the media timeline
	const tpp = int64(27_000)

	// 20 consecutive content packets, one per slot.
	for i := 0; i < 20; i++ {
		p := mergedPkt{data: tsPkt(0x1e1, i&0x0f), ets: anchor + int64(i)*tpp, isVideo: true}
		if err := g.Packet(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}

	// 20 content + 1 PCR (slot 0) = 21 slots = 3 datagrams of 7.
	if len(*out) != 3 {
		t.Fatalf("got %d datagrams, want 3", len(*out))
	}
	for i, d := range *out {
		if len(d.Data) != 12+7*tsPacketSize {
			t.Fatalf("datagram %d size %d", i, len(d.Data))
		}
		// RTP ts follows the slot grid: first slot of datagram i is 7*i.
		wantTs := uint32((anchor + int64(7*i)*tpp) / 300)
		if d.RtpTs != wantTs {
			t.Fatalf("datagram %d RtpTs = %d, want %d", i, d.RtpTs, wantTs)
		}
		if i > 0 && d.Seq != (*out)[i-1].Seq+1 {
			t.Fatalf("seq not continuous at datagram %d", i)
		}
		if d.Discontinuity {
			t.Fatalf("unexpected discontinuity flag on datagram %d", i)
		}
	}

	// Slot 0 is a synthesized PCR packet on the video PID with PCR == outSTC(0).
	pkts := datagramPackets(t, (*out)[0])
	first := pkts[0]
	if first.PID() != 0x1e1 || !first.HasAdaptationField() {
		t.Fatalf("slot 0 is not an adaptation-field packet on the video PID")
	}
	af, err := first.AdaptationField()
	if err != nil {
		t.Fatal(err)
	}
	if has, _ := af.HasPCR(); !has {
		t.Fatal("slot 0 has no PCR")
	}
	pcr, err := af.PCR()
	if err != nil {
		t.Fatal(err)
	}
	if int64(pcr) != anchor {
		t.Fatalf("PCR = %d, want %d", pcr, anchor)
	}
}

func TestPackagerNullPaddingAndPcrCadence(t *testing.T) {
	g, out := collectPackager(t, testClassifier(0x1e1, 0x1e1))

	const anchor = int64(27_000_000)
	const tpp = int64(27_000)

	// Two content packets 70 slots apart: the gap is padded with nulls and PCRs at
	// the 35-slot cadence.
	if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, 0), ets: anchor, isVideo: true}); err != nil {
		t.Fatal(err)
	}
	if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, 1), ets: anchor + 70*tpp, isVideo: true}); err != nil {
		t.Fatal(err)
	}
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}

	var nulls, pcrs, content int
	var pcrSlots []int64
	slot := int64(0)
	for _, d := range *out {
		for _, p := range datagramPackets(t, d) {
			switch {
			case p.PID() == 0x1fff:
				nulls++
			case p.HasAdaptationField() && !p.HasPayload():
				af, _ := p.AdaptationField()
				if has, _ := af.HasPCR(); has {
					pcr, _ := af.PCR()
					if int64(pcr) != anchor+slot*tpp {
						t.Fatalf("PCR at slot %d = %d, want %d", slot, pcr, anchor+slot*tpp)
					}
					pcrSlots = append(pcrSlots, slot)
					pcrs++
				}
			default:
				content++
			}
			slot++
		}
	}
	if content != 2 {
		t.Fatalf("content packets = %d, want 2", content)
	}
	// PCR cadence: every 35 slots across the padded gap (slots 0, 35, 70).
	if pcrs < 3 {
		t.Fatalf("pcr packets = %d (slots %v), want >= 3", pcrs, pcrSlots)
	}
	for i := 1; i < len(pcrSlots); i++ {
		if d := pcrSlots[i] - pcrSlots[i-1]; d > 36 {
			t.Fatalf("PCR gap of %d slots exceeds cadence: %v", d, pcrSlots)
		}
	}
	if nulls == 0 {
		t.Fatal("expected null padding in the gap")
	}
}

func TestPackagerDiscontinuity(t *testing.T) {
	g, out := collectPackager(t, testClassifier(0x1e1, 0x1e1))

	const anchor = int64(27_000_000)
	const tpp = int64(27_000)

	if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, 0), ets: anchor, isVideo: true}); err != nil {
		t.Fatal(err)
	}
	g.NoteDiscontinuity()
	// Content 40 slots later: a PCR is due (>35 slots) and must carry the indicator.
	if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, 1), ets: anchor + 40*tpp, isVideo: true}); err != nil {
		t.Fatal(err)
	}
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}

	foundDiscPCR := false
	discDatagram := false
	for _, d := range *out {
		for _, p := range datagramPackets(t, d) {
			if p.PID() == 0x1e1 && p.HasAdaptationField() && !p.HasPayload() {
				af, _ := p.AdaptationField()
				if has, _ := af.HasPCR(); has {
					if disc, _ := af.Discontinuity(); disc {
						foundDiscPCR = true
						if d.Discontinuity {
							discDatagram = true
						}
					}
				}
			}
		}
	}
	if !foundDiscPCR {
		t.Fatal("no PCR packet carried the discontinuity indicator")
	}
	if !discDatagram {
		t.Fatal("the datagram containing the discontinuity PCR is not flagged")
	}
}

func TestPackagerGridRebaseOnForwardJump(t *testing.T) {
	g, out := collectPackager(t, testClassifier(0x1e1, 0x1e1))

	const anchor = int64(27_000_000)
	const tpp = int64(27_000)

	if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, 0), ets: anchor, isVideo: true}); err != nil {
		t.Fatal(err)
	}
	// A 10s forward jump must rebase the grid, not insert ~10000 null slots.
	if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, 1), ets: anchor + 10*27_000_000, isVideo: true}); err != nil {
		t.Fatal(err)
	}
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}

	totalSlots := 0
	for _, d := range *out {
		totalSlots += (len(d.Data) - 12) / tsPacketSize
	}
	if totalSlots > 10 {
		t.Fatalf("forward jump padded %d slots instead of rebasing", totalSlots)
	}
	// The rebase surfaces as a discontinuity.
	sawDisc := false
	for _, d := range *out {
		if d.Discontinuity {
			sawDisc = true
		}
	}
	if !sawDisc {
		t.Fatal("grid rebase did not flag a discontinuity")
	}
}

func TestPackagerPcrSuppressedOnSeparatePcrPid(t *testing.T) {
	// PCR rides a separate passthrough PID: those packets flow byte-exact, so no PCR
	// may be synthesized on the video PID.
	g, out := collectPackager(t, testClassifier(0x1e1, 0x1f0))

	const anchor = int64(27_000_000)
	const tpp = int64(27_000)
	for i := 0; i < 80; i++ {
		if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, i&0xf), ets: anchor + int64(i)*tpp, isVideo: true}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}

	for _, d := range *out {
		for _, p := range datagramPackets(t, d) {
			if p.PID() == 0x1e1 && p.HasAdaptationField() && !p.HasPayload() {
				t.Fatal("synthesized PCR present despite separate PCR PID")
			}
		}
	}
}

func TestPackagerRtpHeader(t *testing.T) {
	g, out := collectPackager(t, testClassifier(0x1e1, 0x1e1))
	if err := g.Packet(mergedPkt{data: tsPkt(0x1e1, 0), ets: 27_000_000, isVideo: true}); err != nil {
		t.Fatal(err)
	}
	if err := g.Finish(); err != nil {
		t.Fatal(err)
	}
	if len(*out) != 1 {
		t.Fatalf("got %d datagrams", len(*out))
	}
	d := (*out)[0]
	if d.Data[0] != 0x80 {
		t.Fatalf("RTP V/P/X/CC byte = %#x", d.Data[0])
	}
	if d.Data[1]&0x7f != 33 {
		t.Fatalf("payload type = %d, want 33", d.Data[1]&0x7f)
	}
	ssrc := uint32(d.Data[8])<<24 | uint32(d.Data[9])<<16 | uint32(d.Data[10])<<8 | uint32(d.Data[11])
	if ssrc != 0xabcd1234 {
		t.Fatalf("ssrc = %#x", ssrc)
	}
}
