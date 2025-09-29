/*
 * Defines various audio/video transcoding errors.
 */
package avpipe

// #cgo pkg-config: libavcodec
// #cgo pkg-config: libavfilter
// #cgo pkg-config: libavformat
// #cgo pkg-config: libavutil
// #cgo pkg-config: libswresample
// #cgo pkg-config: libavdevice
// #cgo pkg-config: libswscale
// #cgo pkg-config: libavutil
// #cgo pkg-config: libpostproc
// #cgo netint pkg-config: xcoder
// #cgo pkg-config: srt
// #cgo CFLAGS: -I${SRCDIR}/include
// #cgo CFLAGS: -I${SRCDIR}/libavpipe/include
// #cgo CFLAGS: -I${SRCDIR}/utils/include
// #cgo LDFLAGS: -L${SRCDIR}
// #cgo linux LDFLAGS: -Wl,-rpath,$ORIGIN/../lib
// #include "avpipe.h"
import "C"

import (
	"errors"

	"github.com/eluv-io/avpipe/goavpipe"
)

// EAV_FILTER_STRING_INIT is the error returned when avpipe fails to obtain filter string.
var EAV_FILTER_STRING_INIT = errors.New("EAV_FILTER_STRING_INIT")

// EAV_MEM_ALLOC is the error returned when avpipe fails to allocate memory for
// a frame, or a packet, or a context (i.e format context).
var EAV_MEM_ALLOC = errors.New("EAV_MEM_ALLOC")

// EAV_FILTER_INIT is the error returned when avpipe fails to initialize a filter.
var EAV_FILTER_INIT = errors.New("EAV_FILTER_INIT")

// EAV_NUM_STREAMS is the error returned when the number of streams are wrong
// while avpipe does transcoding
var EAV_NUM_STREAMS = errors.New("EAV_NUM_STREAMS")

// EAV_WRITE_HEADER is the error returned when avpipe fails to write the output
// stream header.
var EAV_WRITE_HEADER = errors.New("EAV_WRITE_HEADER")

// EAV_TIMEBASE is the error returned when the timebase is not correct.
// This can happen if the calculated codec context timebase doesn't match
// with output stream timebase.
var EAV_TIMEBASE = errors.New("EAV_TIMEBASE")

// EAV_SEEK is the error returned when avpipe fails to seek.
var EAV_SEEK = errors.New("EAV_SEEK")

// EAV_CANCELLED is the error returned when a transcoding session is cancelled.
// This error is returned from Tx() or TxRun() if transcoding session is cancelled
// by calling TxCancel() and TxCancel() was successful.
var EAV_CANCELLED = errors.New("EAV_CANCELLED")

// EAV_CANCEL_FAILED is the error returned when cancelling a transcoding session fails.
// This error is returned from TxCancel() if cancelling a transcoding fails.
var EAV_CANCEL_FAILED = errors.New("EAV_CANCEL_FAILED")

// EAV_OPEN_INPUT is the error returned when avpipe fails to open the input or
// stream container due to invalid/corrupted data
var EAV_OPEN_INPUT = errors.New("EAV_OPEN_INPUT")

// EAV_STREAM_INFO is the error returned when avpipe fails to obtain stream info.
var EAV_STREAM_INFO = errors.New("EAV_STREAM_INFO")

// EAV_CODEC_CONTEXT is the error returned when
// - codec context doesn't exist for a stream index or
// - avpipe fails to find codec context for a stream or
// - avpipe fails to allocate codec format context
var EAV_CODEC_CONTEXT = errors.New("EAV_CODEC_CONTEXT")

// EAV_CODEC_PARAM is the error returned when avpipe fails
// - to copy codec params to codec context for output stream or
// - codec parameters are not correct
var EAV_CODEC_PARAM = errors.New("EAV_CODEC_PARAM")

// EAV_OPEN_CODEC is the error returned when avpipe fails to open the codec.
var EAV_OPEN_CODEC = errors.New("EAV_OPEN_CODEC")

// EAV_PARAM is the error returned when one of the transcoding params is not correct.
var EAV_PARAM = errors.New("EAV_PARAM")

// EAV_STREAM_INDEX is the error returned when stream index is not correct.
var EAV_STREAM_INDEX = errors.New("EAV_STREAM_INDEX")

// EAV_READ_INPUT is the error returned when avpipe fails to read input packets.
var EAV_READ_INPUT = errors.New("EAV_READ_INPUT")

// EAV_SEND_PACKET is the error returned when avpipe fails to send packet to decoder.
// This error means the packet is invalid.
var EAV_SEND_PACKET = errors.New("EAV_SEND_PACKET")

// EAV_RECEIVE_FRAME is the error returned when avpipe fails to receive frame
// from decoder or audio fifo.
var EAV_RECEIVE_FRAME = errors.New("EAV_RECEIVE_FRAME")

// EAV_RECEIVE_FILTER_FRAME is the error returned when avpipe fails to receive frame
// from filter.
var EAV_RECEIVE_FILTER_FRAME = errors.New("EAV_RECEIVE_FILTER_FRAME")

// EAV_RECEIVE_PACKET is the error returned when avpipe fails to receive packets
// from encoder.
var EAV_RECEIVE_PACKET = errors.New("EAV_RECEIVE_PACKET")

// EAV_WRITE_FRAME is the error returned when avpipe fails to write the frame
// into output stream or audio fifo.
var EAV_WRITE_FRAME = errors.New("EAV_WRITE_FRAME")

// EAV_AUDIO_SAMPLE is the error returned when avpipe fails to convert audio samples.
var EAV_AUDIO_SAMPLE = errors.New("EAV_AUDIO_SAMPLE")

// EAV_XC_TABLE is the error returned when avpipe fails to find xc context in the xc_table.
var EAV_XC_TABLE = errors.New("EAV_XC_TABLE")

// EAV_PTS_WRAPPED is the error returned when PTS is wrapped in the source/input.
var EAV_PTS_WRAPPED = errors.New("EAV_PTS_WRAPPED")

// EAV_IO_TIMEOUT is the error returned when there is a timeout in network/disk io
var EAV_IO_TIMEOUT = errors.New("EAV_IO_TIMEOUT")

// EAV_BAD_HANDLE is the error returned when the transcoding session handle is not valid
var EAV_BAD_HANDLE = errors.New("EAV_BAD_HANDLE")

// EAV_UNKNOWN is the error returned when error code doesn't exist in avpipeErrors table (below).
var EAV_UNKNOWN = errors.New("EAV_UNKNOWN")

var avpipeErrors = map[int]error{
	int(C.eav_filter_string_init):   EAV_FILTER_STRING_INIT,
	int(C.eav_mem_alloc):            EAV_MEM_ALLOC,
	int(C.eav_filter_init):          EAV_FILTER_INIT,
	int(C.eav_num_streams):          EAV_NUM_STREAMS,
	int(C.eav_write_header):         EAV_WRITE_HEADER,
	int(C.eav_timebase):             EAV_TIMEBASE,
	int(C.eav_seek):                 EAV_SEEK,
	int(C.eav_cancelled):            EAV_CANCELLED,
	int(C.eav_open_input):           EAV_OPEN_INPUT,
	int(C.eav_read_input):           EAV_READ_INPUT,
	int(C.eav_stream_info):          EAV_STREAM_INFO,
	int(C.eav_codec_context):        EAV_CODEC_CONTEXT,
	int(C.eav_codec_param):          EAV_CODEC_PARAM,
	int(C.eav_open_codec):           EAV_OPEN_CODEC,
	int(C.eav_param):                EAV_PARAM,
	int(C.eav_stream_index):         EAV_STREAM_INDEX,
	int(C.eav_send_packet):          EAV_SEND_PACKET,
	int(C.eav_receive_frame):        EAV_RECEIVE_FRAME,
	int(C.eav_receive_filter_frame): EAV_RECEIVE_FILTER_FRAME,
	int(C.eav_receive_packet):       EAV_RECEIVE_PACKET,
	int(C.eav_write_frame):          EAV_WRITE_FRAME,
	int(C.eav_audio_sample):         EAV_AUDIO_SAMPLE,
	int(C.eav_xc_table):             EAV_XC_TABLE,
	int(C.eav_pts_wrapped):          EAV_PTS_WRAPPED,
	int(C.eav_io_timeout):           EAV_IO_TIMEOUT,
	int(C.eav_bad_handle):           EAV_BAD_HANDLE,
}

func avpipeError(code C.int) error {
	// Error code 0 means success
	if code == 0 {
		return nil
	}

	err, ok := avpipeErrors[int(code)]
	if !ok {
		goavpipe.Log.Debug("avpipeError unknown", "code", int(code))
		return EAV_UNKNOWN
	}

	return err
}
