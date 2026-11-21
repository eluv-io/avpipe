package mpegtsxc

import (
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Comcast/gots/v2/packet"

	"github.com/eluv-io/avpipe"
)

// TestPartsTranscoderRoundTrip is the end-to-end parts-mode test: a generated CBR
// MPEGTS asset is wrapped in RTP datagrams (the recorded-part surrogate), run through
// the transcoder, and the output verified for grid-exact RTP timestamps, seq
// continuity, PCR cadence, and byte-identical non-video packets.
func TestPartsTranscoderRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping transcode round-trip")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}

	avpipe.SetCLoggers()

	// Generate a 10s CBR MPEGTS asset: H264 video + AAC audio, muxrate 3 Mbps.
	src := filepath.Join(t.TempDir(), "src.ts")
	const muxrate = 3_000_000
	cmd := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1280x720:rate=30:duration=10",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=10",
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", "2000000",
		"-x264-params", "keyint=30:min-keyint=30:nal-hrd=cbr",
		"-maxrate", "2000000", "-bufsize", "2000000",
		"-c:a", "aac", "-b:a", "128000",
		"-muxrate", "3000000", "-f", "mpegts", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg source generation failed: %v\n%s", err, out)
	}
	tsData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	tsData = tsData[:len(tsData)/188*188]
	t.Logf("source: %d TS packets", len(tsData)/188)

	// Wrap in RTP datagrams of 7 TS packets: RTP ts from the byte position at
	// muxrate (a CBR virtual clock, like a broadcast encoder's emission times).
	var datagrams [][]byte
	seq := uint16(1000)
	for off := 0; off < len(tsData); off += 7 * 188 {
		end := off + 7*188
		if end > len(tsData) {
			end = len(tsData)
		}
		dg := make([]byte, 12+end-off)
		dg[0] = 0x80
		dg[1] = 33 // MP2T payload type
		binary.BigEndian.PutUint16(dg[2:4], seq)
		ts90k := uint32(int64(off) * 8 * 90000 / muxrate)
		binary.BigEndian.PutUint32(dg[4:8], ts90k)
		binary.BigEndian.PutUint32(dg[8:12], 0x1234abcd)
		copy(dg[12:], tsData[off:end])
		datagrams = append(datagrams, dg)
		seq++
	}

	const streamBitrate = 3_500_000
	var mu sync.Mutex
	var out []OutputDatagram
	xc, err := NewPartsTranscoder(nil, Config{
		EncWidth:      640,
		EncHeight:     360,
		Ecodec:        "libx264",
		VideoBitrate:  1_000_000,
		StreamBitrate: streamBitrate,
	}, func(d OutputDatagram) error {
		mu.Lock()
		out = append(out, d)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	for _, dg := range datagrams {
		if err := xc.Feed(dg); err != nil {
			t.Fatalf("Feed: %v", err)
		}
	}
	if err := xc.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	t.Logf("transcode of 10s took %s; %d output datagrams, stats: %+v",
		time.Since(start), len(out), xc.Stats())

	if len(out) < 100 {
		t.Fatalf("suspiciously few output datagrams: %d", len(out))
	}
	if xc.Stats().Discontinuities != 0 {
		t.Fatalf("unexpected discontinuities: %d", xc.Stats().Discontinuities)
	}

	// 1. RTP timestamps follow the CBR slot grid (RtpTs is outSTC/300 truncated, so
	// per-datagram deltas may vary by 1 tick around the ideal but never drift) and
	// seq is continuous.
	tpp := int64(188*8) * pcrClockHz / streamBitrate
	slots := int64(0)
	for i := 1; i < len(out); i++ {
		if out[i].Seq != out[i-1].Seq+1 {
			t.Fatalf("seq gap at datagram %d: %d -> %d", i, out[i-1].Seq, out[i].Seq)
		}
		slots += int64(len(out[i-1].Data)-12) / 188
		// Cumulative grid check from datagram 0, immune to per-division truncation.
		want := int64(out[0].RtpTs) + slots*tpp/300
		got := int64(out[0].RtpTs) + int64(int32(out[i].RtpTs-out[0].RtpTs))
		if d := got - want; d < -1 || d > 1 {
			t.Fatalf("RTP ts off-grid at datagram %d: got %d, want %d (drift %d)", i, got, want, d)
		}
		if out[i].Discontinuity {
			t.Fatalf("unexpected discontinuity flag at datagram %d", i)
		}
	}

	// Identify PIDs from the source: video PID = the PCR-bearing PES PID resolved by
	// the transcoder's classifier via the source PAT/PMT.
	videoPID := -1
	{
		sn := xc.Stats()
		videoPID = sn.VideoPID
	}
	if videoPID < 0 {
		t.Fatal("video PID not resolved")
	}

	// 2. Non-video packets are byte-identical to the source, in order. The output
	// merge starts at the source's first PCR, so the output non-video sequence must
	// be a contiguous subsequence of the source's.
	extractOther := func(data []byte, skipVideo bool) [][]byte {
		var pkts [][]byte
		for off := 0; off+188 <= len(data); off += 188 {
			p := packet.Packet(data[off : off+188])
			if p.PID() == 0x1fff {
				continue
			}
			if skipVideo && p.PID() == videoPID {
				continue
			}
			b := make([]byte, 188)
			copy(b, data[off:off+188])
			pkts = append(pkts, b)
		}
		return pkts
	}
	srcOther := extractOther(tsData, true)
	var outOther [][]byte
	var pcrPkts []packet.Packet
	var pcrSlots []int64
	slot := int64(0)
	videoPusi := 0
	for _, d := range out {
		for off := 12; off+188 <= len(d.Data); off += 188 {
			var p packet.Packet
			copy(p[:], d.Data[off:off+188])
			switch {
			case p.PID() == 0x1fff:
			case p.PID() == videoPID:
				if p.HasAdaptationField() && !p.HasPayload() {
					pcrPkts = append(pcrPkts, p)
					pcrSlots = append(pcrSlots, slot)
				} else if p.PayloadUnitStartIndicator() {
					videoPusi++
				}
			default:
				b := make([]byte, 188)
				copy(b, p[:])
				outOther = append(outOther, b)
			}
			slot++
		}
	}
	if len(outOther) == 0 {
		t.Fatal("no passthrough packets in the output")
	}
	// Find the start of the output sequence within the source sequence.
	startIdx := -1
	for i, sp := range srcOther {
		if string(sp) == string(outOther[0]) {
			startIdx = i
			break
		}
	}
	if startIdx < 0 {
		t.Fatal("first output passthrough packet not found in the source")
	}
	if len(srcOther)-startIdx != len(outOther) {
		t.Fatalf("passthrough packet count: output %d, source %d from offset %d",
			len(outOther), len(srcOther)-startIdx, startIdx)
	}
	for i, op := range outOther {
		if string(op) != string(srcOther[startIdx+i]) {
			t.Fatalf("passthrough packet %d differs from source packet %d", i, startIdx+i)
		}
	}

	// 3. PCR cadence <= 40ms and PCR values on the output grid.
	if len(pcrPkts) < 2 {
		t.Fatalf("too few PCR packets: %d", len(pcrPkts))
	}
	maxCadenceSlots := ticks27(40*time.Millisecond) / tpp
	for i, p := range pcrPkts {
		af, err := p.AdaptationField()
		if err != nil {
			t.Fatal(err)
		}
		if has, _ := af.HasPCR(); !has {
			t.Fatalf("adaptation-only packet %d has no PCR", i)
		}
		if i > 0 {
			if d := pcrSlots[i] - pcrSlots[i-1]; d > maxCadenceSlots {
				t.Fatalf("PCR cadence exceeded at %d: %d slots (max %d)", i, d, maxCadenceSlots)
			}
		}
	}

	// 4. Video was actually re-encoded: an AU per source frame (10s @ 30fps = 300),
	// allowing slack for the encoder's flush behavior.
	if videoPusi < 290 || videoPusi > 310 {
		t.Fatalf("video access units = %d, want ~300", videoPusi)
	}
}

// TestPartsTranscoderConcurrentStop reproduces the production crash of 2026-08-11:
// a feeder goroutine blocked in Feed (pipeline behind, video channel full) while
// another goroutine - having observed the cancellation - calls Finish, which closed
// videoCh under the pending channel send ("panic: send on closed channel"). With
// the feedMu serialization, Cancel unblocks the feeder and Finish waits for it.
func TestPartsTranscoderConcurrentStop(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping transcode round-trip")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	avpipe.SetCLoggers()

	src := filepath.Join(t.TempDir(), "src.ts")
	cmd := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=4",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=4",
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", "1000000",
		"-c:a", "aac", "-b:a", "96000",
		"-muxrate", "2000000", "-f", "mpegts", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg source generation failed: %v\n%s", err, out)
	}
	tsData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	tsData = tsData[:len(tsData)/188*188]

	const muxrate = 2_000_000
	for round := 0; round < 3; round++ {
		xc, err := NewPartsTranscoder(nil, Config{
			EncWidth:      320,
			EncHeight:     180,
			Ecodec:        "libx264",
			VideoBitrate:  500_000,
			StreamBitrate: 2_500_000,
		}, func(d OutputDatagram) error { return nil })
		if err != nil {
			t.Fatal(err)
		}

		// Feeder goroutine: pump datagrams (cycling over the asset) until the
		// pipeline reports an error - it will spend most time blocked in Feed.
		feederDone := make(chan struct{})
		go func() {
			defer close(feederDone)
			seq := uint16(0)
			for {
				for off := 0; off < len(tsData); off += 7 * 188 {
					end := off + 7*188
					if end > len(tsData) {
						end = len(tsData)
					}
					dg := make([]byte, 12+end-off)
					dg[0] = 0x80
					dg[1] = 33
					binary.BigEndian.PutUint16(dg[2:4], seq)
					binary.BigEndian.PutUint32(dg[4:8], uint32(int64(off)*8*90000/muxrate))
					binary.BigEndian.PutUint32(dg[8:12], 0x1234abcd)
					copy(dg[12:], tsData[off:end])
					seq++
					if err := xc.Feed(dg); err != nil {
						return
					}
				}
			}
		}()

		// Let the feeder get going (and likely block on backpressure), then stop
		// from this goroutine - the production stop path (cancel, then finish,
		// concurrent with a parked Feed).
		time.Sleep(300 * time.Millisecond)
		xc.Cancel()
		if err := xc.Finish(); err == nil {
			t.Fatalf("round %d: Finish after Cancel returned nil error", round)
		}

		select {
		case <-feederDone:
		case <-time.After(10 * time.Second):
			t.Fatalf("round %d: feeder did not unblock after Cancel+Finish", round)
		}
	}
}

// TestPartsTranscoderDiscontinuity injects a seq/ts gap mid-stream and verifies the
// discontinuity is flagged while the output timeline stays monotonic and on-grid.
func TestPartsTranscoderDiscontinuity(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode: skipping transcode round-trip")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found in PATH")
	}
	avpipe.SetCLoggers()

	src := filepath.Join(t.TempDir(), "src.ts")
	cmd := exec.Command(ffmpeg, "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-c:v", "libx264", "-preset", "veryfast", "-b:v", "1000000",
		"-x264-params", "keyint=30:min-keyint=30",
		"-c:a", "aac", "-b:a", "96000",
		"-muxrate", "2000000", "-f", "mpegts", src)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg source generation failed: %v\n%s", err, out)
	}
	tsData, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	tsData = tsData[:len(tsData)/188*188]

	const muxrate = 2_000_000
	var mu sync.Mutex
	var out []OutputDatagram
	xc, err := NewPartsTranscoder(nil, Config{
		EncWidth:      320,
		EncHeight:     180,
		Ecodec:        "libx264",
		VideoBitrate:  500_000,
		StreamBitrate: 2_500_000,
	}, func(d OutputDatagram) error {
		mu.Lock()
		out = append(out, d)
		mu.Unlock()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	seq := uint16(0)
	n := 0
	injected := false
	var tsJump uint32
	for off := 0; off < len(tsData); off += 7 * 188 {
		end := off + 7*188
		if end > len(tsData) {
			end = len(tsData)
		}
		dg := make([]byte, 12+end-off)
		dg[0] = 0x80
		dg[1] = 33
		if n > 500 && !injected {
			// Injected gap after ~500 datagrams: seq jump + persistent 5s ts jump.
			injected = true
			seq += 200
			tsJump = 5 * 90000
		}
		ts90k := uint32(int64(off)*8*90000/muxrate) + tsJump
		binary.BigEndian.PutUint16(dg[2:4], seq)
		binary.BigEndian.PutUint32(dg[4:8], ts90k)
		binary.BigEndian.PutUint32(dg[8:12], 0x1234abcd)
		copy(dg[12:], tsData[off:end])
		seq++
		n++
		if err := xc.Feed(dg); err != nil {
			t.Fatalf("Feed: %v", err)
		}
	}
	if err := xc.Finish(); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if xc.Stats().Discontinuities != 1 {
		t.Fatalf("discontinuities = %d, want 1", xc.Stats().Discontinuities)
	}
	// Output timeline monotonic and seq continuous throughout.
	for i := 1; i < len(out); i++ {
		if out[i].Seq != out[i-1].Seq+1 {
			t.Fatalf("output seq gap at %d", i)
		}
		if int32(out[i].RtpTs-out[i-1].RtpTs) < 0 {
			t.Fatalf("output RTP ts went backwards at %d", i)
		}
	}
}
