package mpegtsxc

import (
	"strings"
	"testing"
	"time"

	"github.com/Comcast/gots/v2/packet"
)

func tsPkt(pid int, cc int) packet.Packet {
	var p packet.Packet
	for i := range p {
		p[i] = 0xff
	}
	p[0] = 0x47
	p[1] = byte((pid >> 8) & 0x1f)
	p[2] = byte(pid & 0xff)
	p[3] = 0x10 | byte(cc&0x0f) // payload only
	return p
}

func TestMuxMergeExactOrder(t *testing.T) {
	fifo := NewPassthroughFifo(64)
	videoCh := make(chan videoPkt, 64)

	otherEts := []int64{100, 200, 300, 400, 500}
	videoEts := []int64{150, 250, 260, 450}
	for i, e := range otherEts {
		fifo.Push(tsItem{data: tsPkt(0x100, i), ets: e})
	}
	fifo.Close()
	for i, e := range videoEts {
		videoCh <- videoPkt{data: tsPkt(0x1e1, i), ets: e, dts: e / 300}
	}
	close(videoCh)

	var got []int64
	var kinds []bool
	err := muxMerge(fifo, videoCh, func(p mergedPkt) error {
		got = append(got, p.ets)
		kinds = append(kinds, p.isVideo)
		return nil
	})
	if err != nil {
		t.Fatalf("muxMerge: %v", err)
	}

	want := []int64{100, 150, 200, 250, 260, 300, 400, 450, 500}
	if len(got) != len(want) {
		t.Fatalf("got %d packets, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order mismatch at %d: got %v, want %v", i, got, want)
		}
	}
	// Spot-check the kinds: 150/250/260/450 are video.
	wantVideo := map[int64]bool{150: true, 250: true, 260: true, 450: true}
	for i, e := range got {
		if kinds[i] != wantVideo[e] {
			t.Fatalf("kind mismatch at ets %d", e)
		}
	}
}

func TestMuxMergeTieGoesToPassthrough(t *testing.T) {
	fifo := NewPassthroughFifo(4)
	videoCh := make(chan videoPkt, 4)
	fifo.Push(tsItem{data: tsPkt(0x100, 0), ets: 100})
	fifo.Close()
	videoCh <- videoPkt{data: tsPkt(0x1e1, 0), ets: 100}
	close(videoCh)

	var kinds []bool
	if err := muxMerge(fifo, videoCh, func(p mergedPkt) error {
		kinds = append(kinds, p.isVideo)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 2 || kinds[0] || !kinds[1] {
		t.Fatalf("tie order wrong: %v", kinds)
	}
}

func TestMuxMergeHoldsForVideo(t *testing.T) {
	// Passthrough is available immediately; video arrives late. The merge must not
	// emit anything until the video side bounds the timeline.
	fifo := NewPassthroughFifo(64)
	videoCh := make(chan videoPkt)

	for i := 0; i < 5; i++ {
		fifo.Push(tsItem{data: tsPkt(0x100, i), ets: int64(100 + i*100)})
	}

	emitted := make(chan int64, 16)
	done := make(chan error, 1)
	go func() {
		done <- muxMerge(fifo, videoCh, func(p mergedPkt) error {
			emitted <- p.ets
			return nil
		})
	}()

	select {
	case e := <-emitted:
		t.Fatalf("emitted ets %d before video arrived", e)
	case <-time.After(50 * time.Millisecond):
	}

	videoCh <- videoPkt{data: tsPkt(0x1e1, 0), ets: 450}
	close(videoCh)
	fifo.Close()

	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(emitted)
	var got []int64
	for e := range emitted {
		got = append(got, e)
	}
	want := []int64{100, 200, 300, 400, 450, 500}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestMuxMergeStallWatchdog(t *testing.T) {
	orig := stallTimeout
	stallTimeout = 20 * time.Millisecond
	defer func() { stallTimeout = orig }()

	fifo := NewPassthroughFifo(2)
	videoCh := make(chan videoPkt) // never produces, never closes

	fifo.Push(tsItem{data: tsPkt(0x100, 0), ets: 100})
	fifo.Push(tsItem{data: tsPkt(0x100, 1), ets: 200})
	// Refill the FIFO once the merge pops its lookahead, so it stays full while the
	// merge waits on the (dead) video leg — the stall condition.
	go fifo.PushWait(tsItem{data: tsPkt(0x100, 2), ets: 300})

	err := muxMerge(fifo, videoCh, func(p mergedPkt) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("expected stall error, got %v", err)
	}
}
