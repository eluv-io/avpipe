package avpipe

// #include <stdlib.h>
// #include "avpipe_anc.h"
import "C"

import (
	"errors"
	"fmt"
	"unsafe"

	"github.com/eluv-io/avpipe/goavpipe"
)

// AncStream describes the ancillary data stream a scan reads.
type AncStream struct {
	StreamIndex int
	StreamID    int    // TS PID
	CodecName   string // "smpte_2038", or what the demuxer made of a stream picked by id ("bin_data")
	CodecTag    string // the registration descriptor's format identifier, "VANC" for ST 2038
}

var ErrNoAncStream = errors.New("avpipe: no SMPTE ST 2038 data stream")

// ScanANC reads the ancillary data stream of url through libavformat and calls fn with every PES packet, as the
// AV_IN_STAT_DATA_ANC stat reports it, without decoding or transcoding anything. The stream is the one a
// transcode picks: the smpte_2038 data stream, or with ancStreamID the data stream with that id (TS PID), for an
// encoder whose PMT does not mark the stream (XcParams.AncStreamId). Pts values are the demuxer's, not unwrapped.
// An error from fn ends the scan and is returned. "pipe:0" reads standard input.
func ScanANC(url string, ancStreamID int, fn func(*goavpipe.AncPacket) error) (AncStream, error) {
	curl := C.CString(url)
	defer C.free(unsafe.Pointer(curl))

	var scan *C.anc_scan_t
	switch rc := C.anc_scan_open(curl, C.int(ancStreamID), &scan); rc {
	case 0:
	case -2:
		return AncStream{}, ErrNoAncStream
	default:
		return AncStream{}, fmt.Errorf("avpipe: cannot read %s", url)
	}
	defer C.anc_scan_close(scan)

	stream := AncStream{
		StreamIndex: int(scan.stream_index),
		StreamID:    int(scan.stream_id),
		CodecName:   C.GoString(&scan.codec_name[0]),
		CodecTag:    C.GoString(&scan.codec_tag[0]),
	}
	var pkt C.anc_packet_t
	for {
		rc := C.anc_scan_next(scan, &pkt)
		if rc == 0 {
			return stream, nil
		}
		if rc < 0 {
			return stream, fmt.Errorf("avpipe: reading %s: error %d", url, int(rc))
		}
		err := fn(&goavpipe.AncPacket{
			Pts:         int64(pkt.pts),
			Dts:         int64(pkt.dts),
			TimeBaseNum: int(pkt.time_base_num),
			TimeBaseDen: int(pkt.time_base_den),
			Payload:     C.GoBytes(unsafe.Pointer(pkt.data), pkt.size),
		})
		if err != nil {
			return stream, err
		}
	}
}
