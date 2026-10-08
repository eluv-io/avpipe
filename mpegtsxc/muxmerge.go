package mpegtsxc

import (
	"fmt"
	"time"

	"github.com/Comcast/gots/v2/packet"
)

// mergedPkt is one TS packet on the merged output timeline.
type mergedPkt struct {
	data    packet.Packet
	ets     int64 // emission timestamp, 27 MHz on the media timeline
	isVideo bool
}

// stallTimeout bounds how long the merge waits for the video leg while the
// passthrough FIFO is completely full — that combination means the encoder is wedged
// (or drastically under-provisioned) and upstream Feed is blocked, so failing the job
// beats deadlocking it. (var so tests can shorten it)
var stallTimeout = 30 * time.Second

// muxMerge merges the passthrough packets with the re-encoded video packets in exact
// ets order (parts mode). Unlike the live-mode lead-window mux it never drops and
// never lets packets reorder: when one side has no packet pending it blocks until
// that side produces or closes — backpressure propagates to the caller's Feed.
//
// Correctness of the "emit without waiting" fast paths relies on each side's ets
// being non-decreasing in production order (FIFO tags follow input PCR; video ets
// follow strictly increasing DTS), so the last received ets of a side is a lower
// bound for everything it produces later.
func muxMerge(fifo *PassthroughFifo, videoCh <-chan videoPkt, emit func(mergedPkt) error) error {
	otherC := fifo.Chan()
	otherOpen, videoOpen := true, true

	var pendOther *tsItem
	var pendVideo *videoPkt
	lastOtherEts := int64(-1) << 62 // ets lower bound for future passthrough packets
	lastVideoEts := int64(-1) << 62 // ets lower bound for future video packets

	var nOther, nVideo uint64

	emitOther := func() error {
		err := emit(mergedPkt{data: pendOther.data, ets: pendOther.ets})
		nOther++
		pendOther = nil
		return err
	}
	emitVideo := func() error {
		err := emit(mergedPkt{data: pendVideo.data, ets: pendVideo.ets, isVideo: true})
		nVideo++
		pendVideo = nil
		return err
	}

	for {
		switch {
		case pendOther != nil && pendVideo != nil:
			// Exact merge; tie goes to passthrough (keeps audio marginally ahead).
			var err error
			if pendOther.ets <= pendVideo.ets {
				err = emitOther()
			} else {
				err = emitVideo()
			}
			if err != nil {
				return err
			}

		case pendOther != nil: // no video pending
			// Safe to emit without waiting only if no future video packet can be
			// earlier; otherwise block for the video side (typical while the encoder
			// works through its latency).
			if !videoOpen || pendOther.ets <= lastVideoEts {
				if err := emitOther(); err != nil {
					return err
				}
				continue
			}
			timer := time.NewTimer(stallTimeout)
			select {
			case v, ok := <-videoCh:
				timer.Stop()
				if ok {
					vv := v
					pendVideo = &vv
					lastVideoEts = v.ets
				} else {
					videoOpen = false
				}
			case <-timer.C:
				if fifo.Len() >= fifo.Cap() {
					return fmt.Errorf("mpegts-xc mux: video leg stalled for %s with the passthrough FIFO full", stallTimeout)
				}
			}

		case pendVideo != nil: // no passthrough pending
			if !otherOpen || pendVideo.ets <= lastOtherEts {
				if err := emitVideo(); err != nil {
					return err
				}
				continue
			}
			o, ok := <-otherC
			if ok {
				oo := o
				pendOther = &oo
				lastOtherEts = o.ets
			} else {
				otherOpen = false
			}

		default: // neither side pending
			if !otherOpen && !videoOpen {
				log.Info("mpegts-xc mux merge done", "other_pkts", nOther, "video_pkts", nVideo)
				return nil
			}
			var oc <-chan tsItem
			var vc <-chan videoPkt
			if otherOpen {
				oc = otherC
			}
			if videoOpen {
				vc = videoCh
			}
			select {
			case o, ok := <-oc:
				if ok {
					oo := o
					pendOther = &oo
					lastOtherEts = o.ets
				} else {
					otherOpen = false
				}
			case v, ok := <-vc:
				if ok {
					vv := v
					pendVideo = &vv
					lastVideoEts = v.ets
				} else {
					videoOpen = false
				}
			}
		}
	}
}
