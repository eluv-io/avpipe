/*
Package avpipe has four main interfaces that has to be implemented by the client code:

 1. InputOpener: is the input factory interface that needs an implementation to generate an InputHandler.

 2. InputHandler: is the input handler with Read/Seek/Size/Close methods. An implementation of this
    interface is needed by ffmpeg to process input streams properly.

 3. OutputOpener: is the output factory interface that needs an implementation to generate an OutputHandler.

 4. OutputHandler: is the output handler with Write/Seek/Close methods. An implementation of this
    interface is needed by ffmpeg to write encoded streams properly.
*/
package avpipe

// #cgo pkg-config: libavcodec
// #cgo pkg-config: libavfilter
// #cgo pkg-config: libavformat
// #cgo pkg-config: libavutil
// #cgo pkg-config: libswresample
// #cgo pkg-config: libavresample
// #cgo pkg-config: libavdevice
// #cgo pkg-config: libswscale
// #cgo pkg-config: libavutil
// #cgo pkg-config: libpostproc
// #cgo netint pkg-config: xcoder
// #cgo pkg-config: srt
// #cgo CFLAGS: -I${SRCDIR}/libavpipe/include
// #cgo CFLAGS: -I${SRCDIR}/utils/include
// #cgo LDFLAGS: -L${SRCDIR}
// #cgo linux LDFLAGS: -Wl,-rpath,$ORIGIN/../lib

// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "avpipe.h"
// #include "elv_log.h"
import "C"
import (
	"fmt"
	"io"
	"math/big"
	"math/rand"
	"sync"
	"unsafe"

	"github.com/eluv-io/avpipe/broadcastproto/mpegts"
	"github.com/eluv-io/avpipe/goavpipe"
)

func init() {
	goavpipe.SetXcFn(Xc)
	goavpipe.SetMuxFn(Mux)
	goavpipe.SetXcInitFn(XcInit)
	goavpipe.SetXcRunFn(XcRun)
	goavpipe.SetXcCancelFn(XcCancel)
}

const traceIo bool = false

type SeekReadWriteCloser interface {
	io.Seeker
	io.Reader
	io.Writer
	io.Closer
}

const MaxAudioMux = C.MAX_STREAMS

type SideDataDisplayMatrix struct {
	Type       string  `json:"side_data_type"`
	Rotation   float64 `json:"rotation"`
	RotationCw float64 `json:"rotation_cw"`
}

type StreamInfo struct {
	StreamIndex        int               `json:"stream_index"`
	StreamId           int32             `json:"stream_id"`
	CodecType          string            `json:"codec_type"`
	CodecID            int               `json:"codec_id,omitempty"`
	CodecName          string            `json:"codec_name,omitempty"`
	DurationTs         int64             `json:"duration_ts,omitempty"`
	TimeBase           *big.Rat          `json:"time_base,omitempty"`
	NBFrames           int64             `json:"nb_frames,omitempty"`
	StartTime          int64             `json:"start_time"` // in TS unit
	AvgFrameRate       *big.Rat          `json:"avg_frame_rate,omitempty"`
	FrameRate          *big.Rat          `json:"frame_rate,omitempty"`
	SampleRate         int               `json:"sample_rate,omitempty"`
	Channels           int               `json:"channels,omitempty"`
	ChannelLayout      int               `json:"channel_layout,omitempty"`
	TicksPerFrame      int               `json:"ticks_per_frame,omitempty"`
	BitRate            int64             `json:"bit_rate,omitempty"`
	Has_B_Frames       bool              `json:"has_b_frame"`
	Width              int               `json:"width,omitempty"`  // Video only
	Height             int               `json:"height,omitempty"` // Video only
	PixFmt             int               `json:"pix_fmt"`          // Video only, it matches with enum AVPixelFormat in FFmpeg
	SampleAspectRatio  *big.Rat          `json:"sample_aspect_ratio,omitempty"`
	DisplayAspectRatio *big.Rat          `json:"display_aspect_ratio,omitempty"`
	FieldOrder         string            `json:"field_order,omitempty"`
	Profile            int               `json:"profile,omitempty"`
	Level              int               `json:"level,omitempty"`
	SideData           []interface{}     `json:"side_data,omitempty"`
	Tags               map[string]string `json:"tags,omitempty"`
}

type ContainerInfo struct {
	Duration   float64 `json:"duration"`
	FormatName string  `json:"format_name"`
}

// PENDING: use legacy_imf_dash_extract/media.Probe?
type ProbeInfo struct {
	ContainerInfo ContainerInfo `json:"format"`
	StreamInfo    []StreamInfo  `json:"streams"`
}

// IOHandler defines handlers that will be called from the C interface functions
type IOHandler interface {
	InReader(buf []byte) (int, error)
	InSeeker(offset C.int64_t, whence C.int) error
	InCloser() error
	InStat(stream_index C.int, avp_stat C.avp_stat_t, stat_args *C.void) error
	OutWriter(fd C.int, buf []byte) (int, error)
	OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error)
	OutCloser(fd C.int) error
	OutStat(stream_index C.int, avp_stat C.avp_stat_t, stat_args *C.void) error
}

// Implement IOHandler
type ioHandler struct {
	input    goavpipe.InputHandler // Input file
	mutex    *sync.Mutex
	outTable map[int64]goavpipe.OutputHandler // Map of integer handle to output interfaces
}

func getCIOHandler(fd int64) *ioHandler {
	h, ok := goavpipe.Globals.GetCIOHandler(fd)
	if !ok {
		return nil
	}
	ioh, ok := h.(*ioHandler)
	if !ok {
		return nil
	}
	return ioh
}

//export AVPipeOpenInput
func AVPipeOpenInput(url *C.char, size *C.int64_t) C.int64_t {
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	urlInputOpener := goavpipe.GetInputOpener(filename)
	urlOutputOpener := goavpipe.GetOutputOpener(filename)

	if urlInputOpener == nil || urlOutputOpener == nil {
		goavpipe.Log.Error("Input or output opener(s) are not set", "urlInputOpener", urlInputOpener, "urlOutputOpener", urlOutputOpener)
		return C.int64_t(-1)
	}
	goavpipe.Log.Debug("AVPipeOpenInput()", "url", filename)

	fd := goavpipe.Globals.AssignOutputOpener(urlOutputOpener)

	input, err := urlInputOpener.Open(fd, filename)
	if err != nil {
		return C.int64_t(-1)
	}

	*size = C.int64_t(input.Size())

	h := &ioHandler{input: input, outTable: make(map[int64]goavpipe.OutputHandler), mutex: &sync.Mutex{}}
	goavpipe.Log.Debug("AVPipeOpenInput()", "url", filename, "size", *size, "fd", fd)
	goavpipe.Globals.PutCIOHandler(fd, h)
	return C.int64_t(fd)
}

//export AVPipeOpenMuxInput
func AVPipeOpenMuxInput(out_url, url *C.char, size *C.int64_t) C.int64_t {
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	out_filename := C.GoString((*C.char)(unsafe.Pointer(out_url)))
	urlInputOpener := goavpipe.GetInputOpener(out_filename)
	urlOutputOpener := goavpipe.GetMuxOutputOpener(out_filename)

	goavpipe.Log.Debug("AVPipeOpenMuxInput()", "url", filename, "out_filename", out_filename)

	if urlInputOpener == nil || urlOutputOpener == nil {
		goavpipe.Log.Error("Input or output opener(s) are not set", "urlInputOpener", urlInputOpener, "urlOutputOpener", urlOutputOpener)
		return C.int64_t(-1)
	}

	fd := goavpipe.Globals.GetNextFD()

	input, err := urlInputOpener.Open(fd, filename)
	if err != nil {
		return C.int64_t(-1)
	}

	*size = C.int64_t(input.Size())

	h := &ioHandler{input: input, outTable: make(map[int64]goavpipe.OutputHandler), mutex: &sync.Mutex{}}
	goavpipe.Log.Debug("AVPipeOpenMuxInput()", "url", filename, "size", *size)
	goavpipe.Globals.PutCIOHandler(fd, h)
	return C.int64_t(fd)
}

//export AVPipeReadInput
func AVPipeReadInput(fd C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	h := getCIOHandler(int64(fd))
	if h == nil {
		return C.int(-1)
	}

	if traceIo {
		goavpipe.Log.Debug("AVPipeReadInput()", "fd", fd, "buf", buf, "sz", sz)
	}

	//gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	gobuf := make([]byte, sz)

	n, err := h.InReader(gobuf)
	if n > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
	}

	if err != nil {
		return C.int(-1)
	}

	return C.int(n)
}

func (h *ioHandler) InReader(buf []byte) (int, error) {
	n, err := h.input.Read(buf)

	if traceIo {
		goavpipe.Log.Debug("InReader()", "buf_size", len(buf), "n", n, "error", err)
	}
	return n, err
}

//export AVPipeSeekInput
func AVPipeSeekInput(fd C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	h := getCIOHandler(int64(fd))
	if h == nil {
		return C.int64_t(-1)
	}
	if traceIo {
		goavpipe.Log.Debug("AVPipeSeekInput()", "h", h)
	}

	n, err := h.InSeeker(offset, whence)
	if err != nil {
		return C.int64_t(-1)
	}
	return C.int64_t(n)
}

func (h *ioHandler) InSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.input.Seek(int64(offset), int(whence))
	goavpipe.Log.Debug("InSeeker()", "offset", offset, "whence", whence, "n", n)
	return n, err
}

//export AVPipeCloseInput
func AVPipeCloseInput(fd C.int64_t) C.int {
	h := getCIOHandler(int64(fd))
	if h == nil {
		return C.int(-1)
	}
	err := h.InCloser()

	goavpipe.Globals.DeleteCIOHandlerAndOutputOpeners(int64(fd))
	if err != nil {
		return C.int(-1)
	}

	goavpipe.Log.Debug("AVPipeCloseInput()", "fd", fd)

	return C.int(0)
}

func (h *ioHandler) InCloser() error {
	err := h.input.Close()
	goavpipe.Log.Debug("InCloser()", "error", err)
	return err
}

//export AVPipeStatInput
func AVPipeStatInput(fd C.int64_t, stream_index C.int, avp_stat C.avp_stat_t, stat_args unsafe.Pointer) C.int {
	h := getCIOHandler(int64(fd))
	if h == nil {
		return C.int(-1)
	}

	err := h.InStat(stream_index, avp_stat, stat_args)
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *ioHandler) InStat(stream_index C.int, avp_stat C.avp_stat_t, stat_args unsafe.Pointer) error {
	var err error

	streamIndex := (int)(stream_index)
	switch avp_stat {
	case C.in_stat_bytes_read:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(streamIndex, goavpipe.AV_IN_STAT_BYTES_READ, &statArgs)
	case C.in_stat_decoding_audio_start_pts:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(streamIndex, goavpipe.AV_IN_STAT_DECODING_AUDIO_START_PTS, &statArgs)
	case C.in_stat_decoding_video_start_pts:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(streamIndex, goavpipe.AV_IN_STAT_DECODING_VIDEO_START_PTS, &statArgs)
	case C.in_stat_audio_frame_read:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(streamIndex, goavpipe.AV_IN_STAT_AUDIO_FRAME_READ, &statArgs)
	case C.in_stat_video_frame_read:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(streamIndex, goavpipe.AV_IN_STAT_VIDEO_FRAME_READ, &statArgs)
	case C.in_stat_first_keyframe_pts:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(streamIndex, goavpipe.AV_IN_STAT_FIRST_KEYFRAME_PTS, &statArgs)
	case C.in_stat_data_scte35:
		statArgs := C.GoString((*C.char)(stat_args))
		err = h.input.Stat(streamIndex, goavpipe.AV_IN_STAT_DATA_SCTE35, statArgs)
	}

	return err
}

func (h *ioHandler) putOutTable(fd int64, outHandler goavpipe.OutputHandler) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if outHandler != nil {
		h.outTable[fd] = outHandler
	} else {
		delete(h.outTable, fd)
	}
}

func (h *ioHandler) getOutTable(fd int64) goavpipe.OutputHandler {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	return h.outTable[fd]
}

func getAVType(av_type C.int) goavpipe.AVType {
	switch av_type {
	case C.avpipe_video_init_stream:
		return goavpipe.DASHVideoInit
	case C.avpipe_audio_init_stream:
		return goavpipe.DASHAudioInit
	case C.avpipe_manifest:
		return goavpipe.DASHManifest
	case C.avpipe_video_segment:
		return goavpipe.DASHVideoSegment
	case C.avpipe_audio_segment:
		return goavpipe.DASHAudioSegment
	case C.avpipe_master_m3u:
		return goavpipe.HLSMasterM3U
	case C.avpipe_video_m3u:
		return goavpipe.HLSVideoM3U
	case C.avpipe_audio_m3u:
		return goavpipe.HLSAudioM3U
	case C.avpipe_aes_128_key:
		return goavpipe.AES128Key
	case C.avpipe_mp4_stream:
		return goavpipe.MP4Stream
	case C.avpipe_fmp4_stream:
		return goavpipe.FMP4Stream
	case C.avpipe_mp4_segment:
		return goavpipe.MP4Segment
	case C.avpipe_video_fmp4_segment:
		return goavpipe.FMP4VideoSegment
	case C.avpipe_audio_fmp4_segment:
		return goavpipe.FMP4AudioSegment
	case C.avpipe_mux_segment:
		return goavpipe.MuxSegment
	case C.avpipe_image:
		return goavpipe.FrameImage
	case C.avpipe_mpegts_segment:
		return goavpipe.MpegtsSegment
	default:
		return goavpipe.Unknown
	}
}

// TODO(Nate): Standardize all of these handler functions to be a type conversion wrapper around a
// Go function to have as thin a surface as possible of C functions. This lets these be called
// easily from other code.

//export AVPipeOpenOutput
func AVPipeOpenOutput(handler C.int64_t, stream_index, seg_index C.int, pts C.int64_t, stream_type C.int) C.int64_t {
	return C.int64_t(AVPipeOpenOutputGo(int64(handler), int(stream_index), int(seg_index), int64(pts), getAVType(stream_type)))
}

func AVPipeOpenOutputGo(handler int64, stream_index, seg_index int, pts int64, stream_type goavpipe.AVType) int64 {
	h := getCIOHandler(handler)
	if h == nil {
		goavpipe.Log.Error("AVPipeOpenOutput()", "reason", "handler not found", "handler", handler)
		return -1
	}
	fd := goavpipe.Globals.GetNextFD()
	if stream_type == goavpipe.Unknown {
		goavpipe.Log.Error("AVPipeOpenOutput()", "invalid stream type", stream_type)
		return -1
	}

	outputOpener := goavpipe.GetOutputOpenerByHandler(int64(handler))
	if outputOpener == nil {
		goavpipe.Log.Error("AVPipeOpenOutput() nil outputOpener", "handler", handler)
		return -1
	}
	outHandler, err := outputOpener.Open(int64(handler), fd, int(stream_index), int(seg_index), int64(pts), stream_type)
	if err != nil {
		goavpipe.Log.Error("AVPipeOpenOutput()", "out_type", stream_type, "error", err)
		return -1
	}

	goavpipe.Log.Debug("AVPipeOpenOutput()", "fd", fd, "stream_index", stream_index, "seg_index", seg_index, "pts", pts, "out_type", stream_type)
	h.putOutTable(fd, outHandler)

	return fd
}

//export AVPipeOpenMuxOutput
func AVPipeOpenMuxOutput(url *C.char, stream_type C.int) C.int64_t {
	var out_type goavpipe.AVType

	fd := goavpipe.Globals.GetNextFD()
	switch stream_type {
	case C.avpipe_mp4_segment:
		out_type = goavpipe.MP4Segment
	case C.avpipe_video_fmp4_segment:
		out_type = goavpipe.FMP4VideoSegment
	case C.avpipe_audio_fmp4_segment:
		out_type = goavpipe.FMP4AudioSegment
	default:
		goavpipe.Log.Error("AVPipeOpenOutput()", "invalid stream type", stream_type)
		return C.int64_t(-1)
	}

	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	muxOutputOpener := goavpipe.GetMuxOutputOpener(filename)
	if muxOutputOpener == nil {
		goavpipe.Log.Error("AVPipeOpenMuxOutput() nil muxOutputOpener", "url", filename)
		return C.int64_t(-1)
	}
	outHandler, err := muxOutputOpener.Open(filename, fd, out_type)
	if err != nil {
		goavpipe.Log.Error("AVPipeOpenOutput()", "out_type", out_type, "error", err)
		return C.int64_t(-1)
	}

	goavpipe.Log.Debug("AVPipeOpenOutput()", "fd", fd, "out_type", out_type)
	goavpipe.PutMuxOutputOpener(fd, outHandler)

	return C.int64_t(fd)
}

//export AVPipeWriteOutput
func AVPipeWriteOutput(handler C.int64_t, fd C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	if sz <= 0 {
		return C.int(0)
	}

	//gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	// This should be the equivalent of using GoBytes() but safer if the
	// Go implementation uses C pointer to wrap a slice.
	gobuf := make([]byte, sz)
	C.memcpy(unsafe.Pointer(&gobuf[0]), unsafe.Pointer(buf), C.size_t(sz))

	return C.int(AVPipeWriteOutputGo(int64(handler), int64(fd), gobuf))
}

func AVPipeWriteOutputGo(handler int64, fd int64, buf []byte) int {
	if len(buf) == 0 {
		return 0
	}

	h := getCIOHandler(handler)
	if h == nil {
		goavpipe.Log.Error("AVPipeWriteOutputGo()", "handler not found", "handler", handler)
		return -1
	}

	if traceIo {
		goavpipe.Log.Debug("AVPipeWriteOutputGo", "fd", fd, "buf_size", len(buf))
	}

	if h.getOutTable(fd) == nil {
		msg := fmt.Sprintf("OutWriterX outTable entry is NULL, fd=%d", fd)
		goavpipe.Log.Error(msg)
		return -1
	}

	n, err := h.OutWriter(C.int64_t(fd), buf)
	if err != nil {
		return -1
	}
	return n
}

//export AVPipeWriteMuxOutput
func AVPipeWriteMuxOutput(fd C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	if traceIo {
		goavpipe.Log.Debug("AVPipeWriteMuxOutput", "fd", fd, "sz", sz)
	}

	outHandler := goavpipe.Globals.GetMuxOutputHandler(int64(fd))
	if outHandler == nil {
		return C.int(-1)
	}

	gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	n, err := outHandler.Write(gobuf)
	if err != nil {
		return C.int(-1)
	}

	return C.int(n)
}

func (h *ioHandler) OutWriter(fd C.int64_t, buf []byte) (int, error) {
	outHandler := h.getOutTable(int64(fd))
	n, err := outHandler.Write(buf)
	if traceIo {
		goavpipe.Log.Debug("OutWriter written", "n", n, "error", err)
	}
	return n, err
}

//export AVPipeSeekOutput
func AVPipeSeekOutput(handler C.int64_t, fd C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	h := getCIOHandler(int64(handler))
	if h == nil {
		return C.int64_t(-1)
	}
	n, err := h.OutSeeker(fd, offset, whence)
	if err != nil {
		return C.int64_t(-1)
	}
	return C.int64_t(n)
}

//export AVPipeSeekMuxOutput
func AVPipeSeekMuxOutput(fd C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	outHandler := goavpipe.Globals.GetMuxOutputHandler(int64(fd))
	if outHandler == nil {
		return C.int64_t(-1)
	}

	n, err := outHandler.Seek(int64(offset), int(whence))
	if err != nil {
		return C.int64_t(-1)
	}
	return C.int64_t(n)
}

func (h *ioHandler) OutSeeker(fd C.int64_t, offset C.int64_t, whence C.int) (int64, error) {
	outHandler := h.getOutTable(int64(fd))
	n, err := outHandler.Seek(int64(offset), int(whence))
	goavpipe.Log.Debug("OutSeeker", "err", err)
	return n, err
}

//export AVPipeCloseOutput
func AVPipeCloseOutput(handler C.int64_t, fd C.int64_t) C.int {
	return C.int(AVPipeCloseOutputGo(int64(handler), int64(fd)))
}

func AVPipeCloseOutputGo(handler int64, fd int64) int {
	h := getCIOHandler(handler)
	if h == nil {
		return -1
	}
	defer h.putOutTable(fd, nil)
	err := h.OutCloser(C.int64_t(fd))
	if err != nil {
		return -1
	}

	goavpipe.Log.Debug("AVPipeCloseOutput()", "fd", fd)

	return 0
}

//export AVPipeCloseMuxOutput
func AVPipeCloseMuxOutput(fd C.int64_t) C.int {
	outHandler := goavpipe.Globals.GetMuxOutputHandler(int64(fd))
	if outHandler == nil {
		return C.int(-1)
	}

	err := outHandler.Close()
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *ioHandler) OutCloser(fd C.int64_t) error {
	outHandler := h.getOutTable(int64(fd))
	if outHandler == nil {
		goavpipe.Log.Warn("OutCloser() outHandler already closed", "fd", int64(fd))
		return nil
	}
	err := outHandler.Close()
	goavpipe.Log.Debug("OutCloser()", "fd", int64(fd), "error", err)
	return err
}

//export AVPipeStatOutput
func AVPipeStatOutput(handler C.int64_t,
	fd C.int64_t,
	stream_index C.int,
	buf_type C.avpipe_buftype_t,
	avp_stat C.avp_stat_t,
	stat_args unsafe.Pointer) C.int {

	h := getCIOHandler(int64(handler))
	if h == nil {
		return C.int(-1)
	}

	err := h.OutStat(fd, stream_index, buf_type, avp_stat, stat_args)
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

//export AVPipeStatMuxOutput
func AVPipeStatMuxOutput(fd C.int64_t, stream_index C.int, avp_stat C.avp_stat_t, stat_args unsafe.Pointer) C.int {
	outHandler := goavpipe.Globals.GetMuxOutputHandler(int64(fd))
	if outHandler == nil {
		return C.int(-1)
	}

	streamIndex := (int)(stream_index)
	var err error
	switch avp_stat {
	case C.out_stat_bytes_written:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(streamIndex, goavpipe.MuxSegment, goavpipe.AV_OUT_STAT_BYTES_WRITTEN, &statArgs)
	case C.out_stat_encoding_end_pts:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(streamIndex, goavpipe.MuxSegment, goavpipe.AV_OUT_STAT_ENCODING_END_PTS, &statArgs)
	}

	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

type EncodingFrameStats struct {
	TotalFramesWritten int64 `json:"total_frames_written"`   // Total number of frames encoded in xc session
	FramesWritten      int64 `json:"segment_frames_written"` // Number of frames encoded in current segment
}

func (h *ioHandler) OutStat(fd C.int64_t,
	stream_index C.int,
	av_type C.avpipe_buftype_t,
	avp_stat C.avp_stat_t,
	stat_args unsafe.Pointer) error {

	var err error
	outHandler := h.getOutTable(int64(fd))
	if outHandler == nil {
		return fmt.Errorf("OutStat nil handler, fd=%d", int64(fd))
	}

	streamIndex := (int)(stream_index)
	avType := getAVType(C.int(av_type))
	switch avp_stat {
	case C.out_stat_bytes_written:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(streamIndex, avType, goavpipe.AV_OUT_STAT_BYTES_WRITTEN, &statArgs)
	case C.out_stat_encoding_end_pts:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(streamIndex, avType, goavpipe.AV_OUT_STAT_ENCODING_END_PTS, &statArgs)
	case C.out_stat_start_file:
		statArgs := *(*int)(stat_args)
		err = outHandler.Stat(streamIndex, avType, goavpipe.AV_OUT_STAT_START_FILE, &statArgs)
	case C.out_stat_end_file:
		statArgs := *(*int)(stat_args)
		err = outHandler.Stat(streamIndex, avType, goavpipe.AV_OUT_STAT_END_FILE, &statArgs)
	case C.out_stat_frame_written:
		encodingFramesStats := (*C.encoding_frame_stats_t)(stat_args)
		statArgs := &EncodingFrameStats{
			TotalFramesWritten: int64(encodingFramesStats.total_frames_written),
			FramesWritten:      int64(encodingFramesStats.frames_written),
		}
		err = outHandler.Stat(streamIndex, avType, goavpipe.AV_OUT_STAT_FRAME_WRITTEN, statArgs)
	}

	return err
}

//export GenerateAndRegisterHandle
func GenerateAndRegisterHandle() C.int32_t {
	handle := generateI32Handle()
	goavpipe.AssociateGIDWithHandle(handle)
	return C.int32_t(handle)
}

//export AssociateCThreadWithHandle
func AssociateCThreadWithHandle(handle C.int32_t) C.int {
	if int32(handle) == 0 {
		return C.int(0)
	}
	goavpipe.AssociateGIDWithHandle(int32(handle))
	return C.int(0)
}

//export CLog
func CLog(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	goavpipe.Log.Info(m)
	return C.int(0)
}

//export CDebug
func CDebug(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	goavpipe.Log.Debug(m)
	return C.int(len(m))
}

//export CInfo
func CInfo(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	goavpipe.Log.Info(m)
	return C.int(len(m))
}

//export CWarn
func CWarn(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	goavpipe.Log.Warn(m)
	return C.int(len(m))
}

//export CError
func CError(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	goavpipe.Log.Error(m)
	return C.int(len(m))
}

func SetCLoggers() {
	C.set_loggers()
}

// GetVersion ...
func Version() string {
	return C.GoString((*C.char)(unsafe.Pointer(C.avpipe_version())))
}

func getCParams(params *goavpipe.XcParams) (*C.xcparams_t, error) {
	extractImagesSize := len(params.ExtractImagesTs)

	// same field order as avpipe_xc.h
	cparams := &C.xcparams_t{
		url:                       C.CString(params.Url),
		format:                    C.CString(params.Format),
		start_time_ts:             C.int64_t(params.StartTimeTs),
		start_pts:                 C.int64_t(params.StartPts),
		duration_ts:               C.int64_t(params.DurationTs),
		start_segment_str:         C.CString(params.StartSegmentStr),
		video_bitrate:             C.int(params.VideoBitrate),
		audio_bitrate:             C.int(params.AudioBitrate),
		sample_rate:               C.int(params.SampleRate),
		crf_str:                   C.CString(params.CrfStr),
		preset:                    C.CString(params.Preset),
		rc_max_rate:               C.int(params.RcMaxRate),
		rc_buffer_size:            C.int(params.RcBufferSize),
		audio_seg_duration_ts:     C.int64_t(params.AudioSegDurationTs),
		video_seg_duration_ts:     C.int64_t(params.VideoSegDurationTs),
		seg_duration:              C.CString(params.SegDuration),
		start_fragment_index:      C.int(params.StartFragmentIndex),
		force_keyint:              C.int(params.ForceKeyInt),
		ecodec:                    C.CString(params.Ecodec),
		ecodec2:                   C.CString(params.Ecodec2),
		dcodec:                    C.CString(params.Dcodec),
		dcodec2:                   C.CString(params.Dcodec2),
		enc_height:                C.int(params.EncHeight),
		enc_width:                 C.int(params.EncWidth),
		crypt_iv:                  C.CString(params.CryptIV),
		crypt_key:                 C.CString(params.CryptKey),
		crypt_kid:                 C.CString(params.CryptKID),
		crypt_key_url:             C.CString(params.CryptKeyURL),
		crypt_scheme:              C.crypt_scheme_t(params.CryptScheme),
		xc_type:                   C.xc_type_t(params.XcType),
		watermark_text:            C.CString(params.WatermarkText),
		watermark_timecode:        C.CString(params.WatermarkTimecode),
		watermark_timecode_rate:   C.float(params.WatermarkTimecodeRate),
		watermark_xloc:            C.CString(params.WatermarkXLoc),
		watermark_yloc:            C.CString(params.WatermarkYLoc),
		watermark_relative_sz:     C.float(params.WatermarkRelativeSize),
		watermark_font_color:      C.CString(params.WatermarkFontColor),
		watermark_shadow:          C.int(0),
		watermark_shadow_color:    C.CString(params.WatermarkShadowColor),
		watermark_overlay:         C.CString(params.WatermarkOverlay),
		watermark_overlay_len:     C.int(params.WatermarkOverlayLen),
		watermark_overlay_type:    C.image_type(params.WatermarkOverlayType),
		n_audio:                   C.int(len(params.AudioIndex)),
		channel_layout:            C.int(params.ChannelLayout),
		stream_id:                 C.int(params.StreamId),
		bypass_transcoding:        C.int(0),
		seekable:                  C.int(0),
		max_cll:                   C.CString(params.MaxCLL),
		master_display:            C.CString(params.MasterDisplay),
		bitdepth:                  C.int(params.BitDepth),
		mux_spec:                  C.CString(params.MuxingSpec),
		sync_audio_to_stream_id:   C.int(params.SyncAudioToStreamId),
		gpu_index:                 C.int(params.GPUIndex),
		listen:                    C.int(0),
		connection_timeout:        C.int(params.ConnectionTimeout),
		filter_descriptor:         C.CString(params.FilterDescriptor),
		skip_decoding:             C.int(0),
		use_preprocessed_input:    C.int(0),
		extract_image_interval_ts: C.int64_t(params.ExtractImageIntervalTs),
		extract_images_sz:         C.int(extractImagesSize),
		video_time_base:           C.int(params.VideoTimeBase),
		video_frame_duration_ts:   C.int(params.VideoFrameDurationTs),
		rotate:                    C.int(params.Rotate),
		profile:                   C.CString(params.Profile),
		level:                     C.int(params.Level),
		deinterlace:               C.dif_type(params.Deinterlace),
		timecode:                  C.CString(params.Timecode),

		// All boolean params are handled below
	}

	if params.BypassTranscoding {
		cparams.bypass_transcoding = C.int(1)
	}

	if params.Seekable {
		cparams.seekable = C.int(1)
	}

	if params.WatermarkShadow {
		cparams.watermark_shadow = C.int(1)
	}

	if params.ForceEqualFDuration {
		cparams.force_equal_fduration = C.int(1)
	}

	if params.CopyMpegts {
		cparams.copy_mpegts = C.int(1)
	}

	if params.UseCustomLiveReader {
		cparams.use_preprocessed_input = C.int(1)
	}

	if params.SkipDecoding {
		cparams.skip_decoding = C.int(1)
	}

	if params.Listen {
		cparams.listen = C.int(1)
	}

	if int32(len(params.AudioIndex)) > MaxAudioMux {
		return nil, fmt.Errorf("Invalid number of audio streams NumAudio=%d", len(params.AudioIndex))
	}

	if params.DebugFrameLevel {
		cparams.debug_frame_level = C.int(1)
	}

	for i := 0; i < len(params.AudioIndex); i++ {
		cparams.audio_index[i] = C.int(params.AudioIndex[i])
	}

	if extractImagesSize > 0 {
		C.init_extract_images((*C.xcparams_t)(unsafe.Pointer(cparams)),
			C.int(extractImagesSize))
		for i := 0; i < extractImagesSize; i++ {
			C.set_extract_images((*C.xcparams_t)(unsafe.Pointer(cparams)),
				C.int(i), C.int64_t(params.ExtractImagesTs[i]))
		}
	}

	return cparams, nil
}

func generateI32Handle() int32 {
	// avpipe treats negative handles as evidence of an error, so we generate a non-negative handle
	return rand.Int31()
}

// params: transcoding parameters
func Xc(params *goavpipe.XcParams) error {
	defer goavpipe.XCEnded()
	if params == nil {
		goavpipe.Log.Error("Failed transcoding, params are not set.")
		return EAV_PARAM
	}

	// Convert XcParams to C.txparams_t
	cparams, err := getCParams(params)
	if err != nil {
		goavpipe.Log.Error("Transcoding failed", err, "url", params.Url)
	}

	rc := C.xc((*C.xcparams_t)(unsafe.Pointer(cparams)))

	goavpipe.Globals.RemoveURLHandlers(params.Url)

	return avpipeError(rc)
}

func Mux(params *goavpipe.XcParams) error {
	defer goavpipe.XCEnded()
	if params == nil {
		goavpipe.Log.Error("Failed muxing, params are not set")
		return EAV_PARAM
	}

	params.XcType = goavpipe.XcMux
	cparams, err := getCParams(params)
	if err != nil {
		goavpipe.Log.Error("Muxing failed", err, "url", params.Url)
	}

	rc := C.mux((*C.xcparams_t)(unsafe.Pointer(cparams)))

	goavpipe.Globals.RemoveURLHandlers(params.Url)

	return avpipeError(rc)

}

func ChannelLayoutName(nbChannels, channelLayout int) string {
	channelName := C.avpipe_channel_name(C.int(nbChannels), C.int(channelLayout))
	if unsafe.Pointer(channelName) != C.NULL {
		channelLayoutName := C.GoString((*C.char)(unsafe.Pointer(channelName)))
		return channelLayoutName
	}

	return ""
}

func ChannelLayout(name string) int {
	channelLayout := C.av_get_channel_layout(C.CString(name))
	return int(channelLayout)
}

func GetPixelFormatName(pixFmt int) string {
	pName := C.get_pix_fmt_name(C.int(pixFmt))
	if unsafe.Pointer(pName) != C.NULL {
		pixelFormatName := C.GoString((*C.char)(unsafe.Pointer(pName)))
		return pixelFormatName
	}

	return ""
}

func GetProfileName(codecId int, profile int) string {
	pName := C.get_profile_name(C.int(codecId), C.int(profile))
	if unsafe.Pointer(pName) != C.NULL {
		profileName := C.GoString((*C.char)(unsafe.Pointer(pName)))
		return profileName
	}

	return ""
}

func Probe(params *goavpipe.XcParams) (*ProbeInfo, error) {
	var cprobe *C.xcprobe_t
	var n_streams C.int

	if params == nil {
		goavpipe.Log.Error("Failed probing, params are not set.")
		return nil, EAV_PARAM
	}

	cparams, err := getCParams(params)
	if err != nil {
		goavpipe.Log.Error("Probing failed", err, "url", params.Url)
	}

	rc := C.probe((*C.xcparams_t)(unsafe.Pointer(cparams)), (**C.xcprobe_t)(unsafe.Pointer(&cprobe)), (*C.int)(unsafe.Pointer(&n_streams)))
	if int(rc) != 0 {
		return nil, avpipeError(rc)
	}

	probeInfo := &ProbeInfo{}
	probeInfo.StreamInfo = make([]StreamInfo, int(n_streams))
	probeArray := (*[1 << 10]C.stream_info_t)(unsafe.Pointer(cprobe.stream_info))
	for i := 0; i < int(n_streams); i++ {
		probeInfo.StreamInfo[i].StreamIndex = int(probeArray[i].stream_index)
		probeInfo.StreamInfo[i].StreamId = int32(probeArray[i].stream_id)
		probeInfo.StreamInfo[i].CodecType = goavpipe.AVMediaTypeNames[goavpipe.AVMediaType(probeArray[i].codec_type)]
		probeInfo.StreamInfo[i].CodecID = int(probeArray[i].codec_id)
		probeInfo.StreamInfo[i].CodecName = C.GoString((*C.char)(unsafe.Pointer(&probeArray[i].codec_name)))
		probeInfo.StreamInfo[i].DurationTs = int64(probeArray[i].duration_ts)
		probeInfo.StreamInfo[i].TimeBase = big.NewRat(int64(probeArray[i].time_base.num), int64(probeArray[i].time_base.den))
		probeInfo.StreamInfo[i].NBFrames = int64(probeArray[i].nb_frames)
		probeInfo.StreamInfo[i].StartTime = int64(probeArray[i].start_time)
		if int64(probeArray[i].avg_frame_rate.den) != 0 {
			probeInfo.StreamInfo[i].AvgFrameRate = big.NewRat(int64(probeArray[i].avg_frame_rate.num), int64(probeArray[i].avg_frame_rate.den))
		} else {
			probeInfo.StreamInfo[i].AvgFrameRate = big.NewRat(int64(probeArray[i].avg_frame_rate.num), int64(1))
		}
		if int64(probeArray[i].frame_rate.den) != 0 {
			probeInfo.StreamInfo[i].FrameRate = big.NewRat(int64(probeArray[i].frame_rate.num), int64(probeArray[i].frame_rate.den))
		} else {
			probeInfo.StreamInfo[i].FrameRate = big.NewRat(int64(probeArray[i].frame_rate.num), int64(1))
		}
		probeInfo.StreamInfo[i].SampleRate = int(probeArray[i].sample_rate)
		probeInfo.StreamInfo[i].Channels = int(probeArray[i].channels)
		probeInfo.StreamInfo[i].ChannelLayout = int(probeArray[i].channel_layout)
		probeInfo.StreamInfo[i].TicksPerFrame = int(probeArray[i].ticks_per_frame)
		probeInfo.StreamInfo[i].BitRate = int64(probeArray[i].bit_rate)
		if probeArray[i].has_b_frames > 0 {
			probeInfo.StreamInfo[i].Has_B_Frames = true
		} else {
			probeInfo.StreamInfo[i].Has_B_Frames = false
		}
		probeInfo.StreamInfo[i].Width = int(probeArray[i].width)
		probeInfo.StreamInfo[i].Height = int(probeArray[i].height)
		probeInfo.StreamInfo[i].PixFmt = int(probeArray[i].pix_fmt)
		if int64(probeArray[i].sample_aspect_ratio.den) != 0 {
			probeInfo.StreamInfo[i].SampleAspectRatio = big.NewRat(int64(probeArray[i].sample_aspect_ratio.num), int64(probeArray[i].sample_aspect_ratio.den))
		} else {
			probeInfo.StreamInfo[i].SampleAspectRatio = big.NewRat(int64(probeArray[i].sample_aspect_ratio.num), int64(1))
		}
		if int64(probeArray[i].display_aspect_ratio.den) != 0 {
			probeInfo.StreamInfo[i].DisplayAspectRatio = big.NewRat(int64(probeArray[i].display_aspect_ratio.num), int64(probeArray[i].display_aspect_ratio.den))
		} else {
			probeInfo.StreamInfo[i].DisplayAspectRatio = big.NewRat(int64(probeArray[i].display_aspect_ratio.num), int64(1))
		}
		probeInfo.StreamInfo[i].FieldOrder = goavpipe.AVFieldOrderNames[goavpipe.AVFieldOrder(probeArray[i].field_order)]
		probeInfo.StreamInfo[i].Profile = int(probeArray[i].profile)
		probeInfo.StreamInfo[i].Level = int(probeArray[i].level)

		rot := float64(probeArray[i].side_data.display_matrix.rotation)
		if rot != 0.0 {
			probeInfo.StreamInfo[i].SideData = make([]interface{}, 1)
			displayMatrix := SideDataDisplayMatrix{
				Type:       "Display Matrix",
				Rotation:   rot,
				RotationCw: float64(probeArray[i].side_data.display_matrix.rotation_cw),
			}
			probeInfo.StreamInfo[i].SideData[0] = displayMatrix
		} else {
			probeInfo.StreamInfo[i].SideData = make([]interface{}, 0)
		}

		// Convert AVDictionary data to Tags of type map[string]string using the built in av_dict_get() iterator
		dict := (*C.AVDictionary)(unsafe.Pointer((probeArray[i].tags)))
		var tag *C.AVDictionaryEntry = (*C.AVDictionaryEntry)(unsafe.Pointer(C.av_dict_get(dict, (*C.char)(C.CString("")), (*C.AVDictionaryEntry)(nil), C.AV_DICT_IGNORE_SUFFIX)))
		if tag != nil {
			probeInfo.StreamInfo[i].Tags = map[string]string{}
			for tag != nil {
				probeInfo.StreamInfo[i].Tags[C.GoString((*C.char)(unsafe.Pointer(tag.key)))] = C.GoString((*C.char)(unsafe.Pointer(tag.value)))
				tag = (*C.AVDictionaryEntry)(unsafe.Pointer(C.av_dict_get(dict, (*C.char)(C.CString("")), tag, C.AV_DICT_IGNORE_SUFFIX)))
			}
		}
		C.av_dict_free(&dict)
	}

	probeInfo.ContainerInfo.FormatName = C.GoString((*C.char)(unsafe.Pointer(cprobe.container_info.format_name)))
	probeInfo.ContainerInfo.Duration = float64(cprobe.container_info.duration)

	C.free(unsafe.Pointer(cprobe.stream_info))
	C.free(unsafe.Pointer(cprobe))

	goavpipe.Globals.RemoveURLHandlers(params.Url)

	return probeInfo, nil
}

// Returns a handle and error (if there is any error)
// In case of error the handle would be zero
func XcInit(params *goavpipe.XcParams) (int32, error) {
	// Convert XcParams to C.txparams_t
	if params == nil {
		goavpipe.Log.Error("Failed transcoding, params are not set.")
		return -1, EAV_PARAM
	}

	seqOpenerF := func(inFd int64) mpegts.SequentialOpener {
		// We use 99 as the streamID for mpegts output to avoid collisions with other streams
		// In theory, this writer should not need a streamID, as the mpegts segments are actually a
		// mux of all streams, but the Writing interface requires a streamID.
		return NewAVPipeSequentialOutWriter(inFd, 99, goavpipe.MpegtsSegment)
	}

	// Here we should setup the input opener if specified by the params
	if params.UseCustomLiveReader {
		var opener goavpipe.InputOpener
		var err error
		if params.CopyMpegtsFromInput {
			opener, err = mpegts.NewAutoInputOpener(params.Url, true, seqOpenerF)
			if params.CopyMpegts {
				goavpipe.Log.Warn("XcInit() CopyMpegts is set, but CopyMpegtsFromInput is also set", "url", params.Url)
				params.CopyMpegts = false
			}
		} else {
			opener, err = mpegts.NewAutoInputOpener(params.Url, false, seqOpenerF)
		}
		if err != nil {
			return -1, EAV_PARAM
		}
		goavpipe.InitUrlIOHandlerIfNotPresent(params.Url, opener, nil)
	}

	cparams, err := getCParams(params)
	if err != nil {
		goavpipe.Log.Error("Initializing transcoder failed", err, "url", params.Url)
	}

	var handle C.int32_t
	rc := C.xc_init((*C.xcparams_t)(unsafe.Pointer(cparams)), (*C.int32_t)(unsafe.Pointer(&handle)))
	if rc != C.eav_success {
		return -1, avpipeError(rc)
	}

	return int32(handle), nil
}

func XcRun(handle int32) error {
	defer goavpipe.XCEnded()
	if handle < 0 {
		return EAV_BAD_HANDLE
	}
	goavpipe.AssociateGIDWithHandle(handle)
	rc := C.xc_run(C.int32_t(handle))
	if rc == 0 {
		return nil
	}

	return avpipeError(rc)
}

func XcCancel(handle int32) error {
	rc := C.xc_cancel(C.int32_t(handle))
	if rc == 0 {
		return nil
	}

	return EAV_CANCEL_FAILED
}

// StreamInfoAsArray builds an array where each stream is at its corresponsing index
// by filling in non-existing index positions with codec type "unknown"
func StreamInfoAsArray(s []StreamInfo) []StreamInfo {
	maxIdx := 0
	for _, v := range s {
		if v.StreamIndex > maxIdx {
			maxIdx = v.StreamIndex
		}
	}
	a := make([]StreamInfo, maxIdx+1)
	for i := range a {
		a[i].StreamIndex = i
		a[i].CodecType = goavpipe.AVMediaTypeNames[goavpipe.AVMediaType(goavpipe.AVMEDIA_TYPE_UNKNOWN)]
	}
	for _, v := range s {
		a[v.StreamIndex] = v
	}
	return a
}

func H264GuessLevel(profile int, bitrate int64, framerate, width, height int) int {
	level := C.avpipe_h264_guess_level(
		C.int(profile),
		C.int64_t(bitrate),
		C.int(framerate),
		C.int(width),
		C.int(height))

	return int(level)
}

func H264GuessProfile(bitdepth, width, height int) int {
	profile := C.avpipe_h264_guess_profile(
		C.int(bitdepth),
		C.int(width),
		C.int(height))

	return int(profile)
}
