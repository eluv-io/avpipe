package main

import (
	"io"

	"github.com/Comcast/gots/v2/packet"
)

// videoPkt is a re-encoded video TS packet recovered from avpipe's output
// - already remapped onto the source video PID (CC rewritten, PCR overwritten)
// - dts is the access unit's DTS
type videoPkt struct {
	data packet.Packet
	dts  int64
}

// muxOutput interleaves the passthrough packets with the re-encoded video packets
// - passthrough packets may run at most 'leadTicks' ahead of video
func muxOutput(dest string, out io.WriteCloser, fifo *PassthroughFifo, videoCh <-chan videoPkt, leadTicks int64) error {
	defer out.Close()

	otherC := fifo.Chan()
	otherOpen, videoOpen := true, true

	var pendOther *tsItem
	var pendVideo *videoPkt
	lastVideoDtsStc := int64(-1) // last emitted video DTS in STC units (27Mhz)

	var nOther, nVideo, nHeld uint64

	for {
		// 1. Emit a passthrough packet if inside the lead window (or source ended and video is done)
		if pendOther != nil {
			videoDone := !videoOpen && pendVideo == nil
			fits := lastVideoDtsStc >= 0 && int64(pendOther.pcr) <= lastVideoDtsStc+leadTicks
			if videoDone || fits {
				if _, err := out.Write(pendOther.data[:]); err != nil {
					return err
				}
				nOther++
				pendOther = nil
				continue
			}
		}

		// 2. Otherwise emit one video packet.
		if pendVideo != nil {
			if _, err := out.Write(pendVideo.data[:]); err != nil {
				return err
			}
			nVideo++
			lastVideoDtsStc = pendVideo.dts * 300
			pendVideo = nil
			continue
		}

		// 3. Both inputs drained and closed -> done.
		if !otherOpen && pendOther == nil && !videoOpen && pendVideo == nil {
			break
		}

		// 4. Block to fill a missing lookahead.
		var oc <-chan tsItem
		var vc <-chan videoPkt
		if pendOther == nil && otherOpen {
			oc = otherC
		}
		if pendVideo == nil && videoOpen {
			vc = videoCh
		}
		if oc == nil && vc == nil {
			break // just safety - shouldn't happen
		}
		if pendOther != nil && pendVideo == nil && videoOpen {
			nHeld++ // we're holding an "other" packet, waiting for video to catch up
		}
		select {
		case o, ok := <-oc:
			if ok {
				v := o
				pendOther = &v
			} else {
				otherOpen = false
			}
		case v, ok := <-vc:
			if ok {
				vv := v
				pendVideo = &vv
			} else {
				videoOpen = false
			}
		}
	}

	log.Info("mpegts-xc mux done", "dest", dest,
		"other_pkts", nOther, "video_pkts", nVideo, "waits", nHeld)
	return nil
}
