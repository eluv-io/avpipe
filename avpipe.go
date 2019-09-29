/*
Package avpipe has four main interfaces that has to be implemented by the client code:

  1) InputOpener: is the input factory interface that needs an implementation to generate an InputHandler.

  2) InputHandler: is the input handler with Read/Seek/Size/Close methods. An implementation of this
     interface is needed by ffmpeg to process input streams properly.

  3) OutputOpener: is the output factory interface that needs an implementation to generate an OutputHandler.

  4) OutputHandler: is the output handler with Write/Seek/Close methods. An implementation of this
     interface is needed by ffmpeg to write encoded streams properly.

*/
package avpipe

// #cgo CFLAGS: -I./include
// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "avpipe.h"
// #include "elv_log.h"
import "C"
import (
	elog "eluvio/log"
	"fmt"
	"math/big"
	"sync"
	"unsafe"
)

var log = elog.Get("/eluvio/avpipe")

// AVType ...
type AVType int

const (
	// Unknown 0
	Unknown AVType = iota
	// DASHManifest 1
	DASHManifest
	// DASHVideoInit 2
	DASHVideoInit
	// DASHVideoSegment 3
	DASHVideoSegment
	// DASHAudioInit 4
	DASHAudioInit
	// DASHAudioSegment 5
	DASHAudioSegment
	// HLSMasterM3U 6
	HLSMasterM3U
	// HLSVideoM3U 7
	HLSVideoM3U
	// HLSAudioM3U 8
	HLSAudioM3U
	// AES128Key 9
	AES128Key
	// MP4Stream 10
	MP4Stream
	// FMP4Stream 11 (Fragmented MP4)
	FMP4Stream
)

type TxType int

const (
	TxNone TxType = iota
	TxVideo
	TxAudio
	TxAll
)

// CryptScheme is the content encryption scheme
type CryptScheme int

const (
	// CryptNone - clear
	CryptNone CryptScheme = iota
	// CryptAES128 - AES-128
	CryptAES128
	// CryptCENC - CENC AES-CTR
	CryptCENC
	// CryptCBC1 - CENC AES-CBC
	CryptCBC1
	// CryptCENS - CENC AES-CTR Pattern
	CryptCENS
	// CryptCBCS - CENC AES-CBC Pattern
	CryptCBCS
)

// TxParams should match with txparams_t in avpipe_xc.h
type TxParams struct {
	Format             string      `json:"format,omitempty"`
	StartTimeTs        int32       `json:"start_time_ts,omitempty"`
	SkipOverPts        int32       `json:"skip_over_pts"`
	StartPts           int32       `json:"start_pts,omitempty"` // Start PTS for output
	DurationTs         int32       `json:"duration_ts,omitempty"`
	StartSegmentStr    string      `json:"start_segment_str,omitempty"`
	VideoBitrate       int32       `json:"video_bitrate,omitempty"`
	AudioBitrate       int32       `json:"audio_bitrate,omitempty"`
	SampleRate         int32       `json:"sample_rate,omitempty"` // Audio sampling rate
	CrfStr             string      `json:"crf_str,omitempty"`
	SegDurationTs      int32       `json:"seg_duration_ts,omitempty"`
	SegDurationFr      int32       `json:"seg_duration_fr,omitempty"`
	FrameDurationTs    int32       `json:"frame_duration_ts,omitempty"`
	StartFragmentIndex int32       `json:"start_fragment_index,omitempty"`
	Ecodec             string      `json:"ecodec,omitempty"` // Video encoder
	Dcodec             string      `json:"dcodec,omitempty"` // Video decoder
	EncHeight          int32       `json:"enc_height,omitempty"`
	EncWidth           int32       `json:"enc_width,omitempty"`
	CryptIV            string      `json:"crypt_iv,omitempty"`
	CryptKey           string      `json:"crypt_key,omitempty"`
	CryptKID           string      `json:"crypt_kid,omitempty"`
	CryptKeyURL        string      `json:"crypt_key_url,omitempty"`
	CryptScheme        CryptScheme `json:"crypt_scheme,omitempty"`
	TxType             TxType      `json:"tx_type,omitempty"`
}

type AVMediaType int

const (
	AVMEDIA_TYPE_VIDEO      = 0
	AVMEDIA_TYPE_AUDIO      = 1
	AVMEDIA_TYPE_DATA       = 2 ///< Opaque data information usually continuous
	AVMEDIA_TYPE_SUBTITLE   = 3
	AVMEDIA_TYPE_ATTACHMENT = 4 ///< Opaque data information usually sparse
	AVMEDIA_TYPE_NB         = 5
)

type ProbeInfo struct {
	CodecType          AVMediaType
	CodecID            int
	CodecName          string
	DurationTs         int
	TimeBase           *big.Rat
	NBFrames           int64
	StartTime          int64
	AvgFrameRate       *big.Rat
	FrameRate          *big.Rat
	TicksPerFrame      int
	BitRate            int64
	Has_B_Frames       bool
	Width, Height      int // Video only
	PixFmt             int // Video only, it matches with enum AVPixelFormat in FFmpeg
	SampleAspectRatio  *big.Rat
	DisplayAspectRatio *big.Rat
	FieldOrder         int
}

// IOHandler defines handlers that will be called from the C interface functions
type IOHandler interface {
	InReader(buf []byte) (int, error)
	InSeeker(offset C.int64_t, whence C.int) error
	InCloser() error
	OutWriter(fd C.int, buf []byte) (int, error)
	OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error)
	OutCloser(fd C.int) error
}

type InputOpener interface {
	// fd determines uniquely opening input.
	// url determines input string for transcoding
	Open(fd int64, url string) (InputHandler, error)
}

type InputHandler interface {
	// Reads from input stream into buf.
	// Returns (0, nil) to indicate EOF.
	Read(buf []byte) (int, error)

	// Seeks to specific offset of the input.
	Seek(offset int64, whence int) (int64, error)

	// Closes the input.
	Close() error

	// Returns the size of input, if the size is not known returns 0 or -1.
	Size() int64
}

type OutputOpener interface {
	// h determines uniquely opening input.
	// fd determines uniquely opening output.
	Open(h, fd int64, stream_index, seg_index int, out_type AVType) (OutputHandler, error)
}

type OutputHandler interface {
	// Writes encoded stream to the output.
	Write(buf []byte) (int, error)

	// Seeks to specific offset of the output.
	Seek(offset int64, whence int) (int64, error)

	// Closes the output.
	Close() error
}

// Implement IOHandler
type ioHandler struct {
	input    InputHandler // Input file
	mutex    *sync.Mutex
	outTable map[int64]OutputHandler // Map of integer handle to output interfaces
}

// Global table of handlers
var gHandlers map[int64]*ioHandler = make(map[int64]*ioHandler)
var gHandleNum int64
var gFd int64
var gMutex sync.Mutex
var gInputOpener InputOpener
var gOutputOpener OutputOpener

func InitIOHandler(inputOpener InputOpener, outputOpener OutputOpener) {
	gInputOpener = inputOpener
	gOutputOpener = outputOpener
}

//export NewIOHandler
func NewIOHandler(url *C.char, size *C.int64_t) C.int64_t {
	if gInputOpener == nil || gOutputOpener == nil {
		log.Error("Input or output opener(s) are not set")
		return C.int64_t(-1)
	}
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	log.Debug("NewIOHandler()", "url", filename)

	gMutex.Lock()
	gHandleNum++
	fd := gHandleNum
	gMutex.Unlock()

	input, err := gInputOpener.Open(fd, filename)
	if err != nil {
		return C.int64_t(-1)
	}

	*size = C.int64_t(input.Size())

	h := &ioHandler{input: input, outTable: make(map[int64]OutputHandler), mutex: &sync.Mutex{}}
	log.Debug("NewIOHandler()", "url", filename, "size", *size)

	gMutex.Lock()
	defer gMutex.Unlock()
	gHandlers[fd] = h
	return C.int64_t(fd)
}

//export AVPipeReadInput
func AVPipeReadInput(handler C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

	log.Debug("AVPipeReadInput()", "handler", handler, "buf", buf, "sz", sz)

	//gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	gobuf := make([]byte, sz)

	n, err := h.InReader(gobuf)
	if n > 0 {
		C.memcpy(unsafe.Pointer(buf), unsafe.Pointer(&gobuf[0]), C.size_t(n))
	}

	if err != nil {
		return C.int(-1)
	}

	return C.int(n) // PENDING err
}

func (h *ioHandler) InReader(buf []byte) (int, error) {
	n, err := h.input.Read(buf)
	log.Debug("InReader()", "buf_size", len(buf), "n", n, "error", err)
	return n, err
}

//export AVPipeSeekInput
func AVPipeSeekInput(handler C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int64_t(-1)
	}
	gMutex.Unlock()
	log.Debug("AVPipeSeekInput()", "h", h)

	n, err := h.InSeeker(offset, whence)
	if err != nil {
		return -1
	}
	return C.int64_t(n)
}

func (h *ioHandler) InSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.input.Seek(int64(offset), int(whence))
	log.Debug("InSeeker()", "offset", offset, "whence", whence, "n", n)
	return n, err
}

//export AVPipeCloseInput
func AVPipeCloseInput(handler C.int64_t) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		return C.int(-1)
	}
	err := h.InCloser()

	// Remove the handler from global table
	gHandlers[int64(handler)] = nil
	gMutex.Unlock()
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *ioHandler) InCloser() error {
	err := h.input.Close()
	log.Debug("InCloser()", "error", err)
	return err
}

func (h *ioHandler) putOutTable(fd int64, outHandler OutputHandler) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	h.outTable[fd] = outHandler
}

func (h *ioHandler) getOutTable(fd int64) OutputHandler {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	return h.outTable[fd]
}

//export AVPipeOpenOutput
func AVPipeOpenOutput(handler C.int64_t, stream_index, seg_index, stream_type C.int) C.int64_t {
	var out_type AVType

	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int64_t(-1)
	}
	gFd++
	fd := gFd
	gMutex.Unlock()
	switch stream_type {
	case C.avpipe_video_init_stream:
		out_type = DASHVideoInit
	case C.avpipe_audio_init_stream:
		out_type = DASHAudioInit
	case C.avpipe_manifest:
		out_type = DASHManifest
	case C.avpipe_video_segment:
		out_type = DASHVideoSegment
	case C.avpipe_audio_segment:
		out_type = DASHAudioSegment
	case C.avpipe_master_m3u:
		out_type = HLSMasterM3U
	case C.avpipe_video_m3u:
		out_type = HLSVideoM3U
	case C.avpipe_audio_m3u:
		out_type = HLSAudioM3U
	case C.avpipe_aes_128_key:
		out_type = AES128Key
	case C.avpipe_mp4_stream:
		out_type = MP4Stream
	case C.avpipe_fmp4_stream:
		out_type = FMP4Stream
	default:
		log.Error("AVPipeOpenOutput()", "invalid stream type", stream_type)
		return C.int64_t(-1)
	}

	outHandler, err := gOutputOpener.Open(int64(handler), fd, int(stream_index), int(seg_index), out_type)
	if err != nil {
		log.Error("AVPipeOpenOutput()", "out_type", out_type, "error", err)
		return C.int64_t(-1)
	}

	log.Debug("AVPipeOpenOutput()", "fd", fd, "stream_index", stream_index, "seg_index", seg_index, "out_type", out_type)
	h.putOutTable(fd, outHandler)

	return C.int64_t(fd)
}

//export AVPipeWriteOutput
func AVPipeWriteOutput(handler C.int64_t, fd C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()
	log.Debug("AVPipeWriteOutput", "fd", fd, "sz", sz)

	if h.getOutTable(int64(fd)) == nil {
		msg := fmt.Sprintf("OutWriterX outTable entry is NULL, fd=%d", fd)
		panic(msg)
	}

	gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	n, err := h.OutWriter(fd, gobuf)
	if err != nil {
		return C.int(-1)
	}

	return C.int(n)
}

func (h *ioHandler) OutWriter(fd C.int64_t, buf []byte) (int, error) {
	outHandler := h.getOutTable(int64(fd))
	n, err := outHandler.Write(buf)
	log.Debug("OutWriter written", "n", n, "error", err)
	return n, err
}

//export AVPipeSeekOutput
func AVPipeSeekOutput(handler C.int64_t, fd C.int64_t, offset C.int64_t, whence C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()
	n, err := h.OutSeeker(fd, offset, whence)
	if err != nil {
		return C.int(-1)
	}
	return C.int(n)
}

func (h *ioHandler) OutSeeker(fd C.int64_t, offset C.int64_t, whence C.int) (int64, error) {
	outHandler := h.getOutTable(int64(fd))
	n, err := outHandler.Seek(int64(offset), int(whence))
	log.Debug("OutSeeker", "err", err)
	return n, err
}

//export AVPipeCloseOutput
func AVPipeCloseOutput(handler C.int64_t, fd C.int64_t) C.int {
	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()
	err := h.OutCloser(fd)
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *ioHandler) OutCloser(fd C.int64_t) error {
	outHandler := h.getOutTable(int64(fd))
	err := outHandler.Close()
	log.Debug("OutCloser()", "fd", int64(fd), "error", err)
	return err
}

//export CLog
func CLog(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	log.Info(m)
	return C.int(0)
}

//export CDebug
func CDebug(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	log.Debug(m)
	return C.int(len(m))
}

//export CInfo
func CInfo(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	log.Info(m)
	return C.int(len(m))
}

//export CWarn
func CWarn(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	log.Warn(m)
	return C.int(len(m))
}

//export CError
func CError(msg *C.char) C.int {
	m := C.GoString((*C.char)(unsafe.Pointer(msg)))
	log.Error(m)
	return C.int(len(m))
}

func SetCLoggers() {
	C.set_loggers()
}

// GetVersion ...
func Version() int {
	return int(C.version())
}

// params: transcoding parameters
// url: input filename that has to be transcoded
func Tx(params *TxParams, url string, bypassTranscoding bool, debugFrameLevel bool, lastInputPts *int) int {

	// Convert TxParams to C.txparams_t
	if params == nil {
		log.Error("Failed transcoding, params is not set.")
		return -1
	}

	cparams := &C.txparams_t{
		format:               C.CString(params.Format),
		start_time_ts:        C.int(params.StartTimeTs),
		skip_over_pts:        C.int(params.SkipOverPts),
		start_pts:            C.int(params.StartPts),
		duration_ts:          C.int(params.DurationTs),
		start_segment_str:    C.CString(params.StartSegmentStr),
		video_bitrate:        C.int(params.VideoBitrate),
		audio_bitrate:        C.int(params.AudioBitrate),
		sample_rate:          C.int(params.SampleRate),
		crf_str:              C.CString(params.CrfStr),
		seg_duration_ts:      C.int(params.SegDurationTs),
		seg_duration_fr:      C.int(params.SegDurationFr),
		frame_duration_ts:    C.int(params.FrameDurationTs),
		start_fragment_index: C.int(params.StartFragmentIndex),
		ecodec:               C.CString(params.Ecodec),
		dcodec:               C.CString(params.Dcodec),
		enc_height:           C.int(params.EncHeight),
		enc_width:            C.int(params.EncWidth),
		crypt_iv:             C.CString(params.CryptIV),
		crypt_key:            C.CString(params.CryptKey),
		crypt_kid:            C.CString(params.CryptKID),
		crypt_key_url:        C.CString(params.CryptKeyURL),
		crypt_scheme:         C.crypt_scheme_t(params.CryptScheme),
		tx_type:              C.tx_type_t(params.TxType),
	}

	var bypass int
	if bypassTranscoding {
		bypass = 1
	} else {
		bypass = 0
	}

	var debugFrameLevelInt int
	if debugFrameLevel {
		debugFrameLevelInt = 1
	} else {
		debugFrameLevelInt = 0
	}

	var lastInputPtsC C.int

	rc := C.tx((*C.txparams_t)(unsafe.Pointer(cparams)), C.CString(url), C.int(bypass), C.int(debugFrameLevelInt),
		&lastInputPtsC)

	*lastInputPts = int(lastInputPtsC)

	return int(rc)
}

func AVMediaTypeName(mediaType AVMediaType) string {
	switch mediaType {
	case AVMEDIA_TYPE_VIDEO:
		return "video"
	case AVMEDIA_TYPE_AUDIO:
		return "audio"
	case AVMEDIA_TYPE_DATA:
		return "data"
	case AVMEDIA_TYPE_SUBTITLE:
		return "subtitle"
	case AVMEDIA_TYPE_ATTACHMENT:
		return "attachment"
	}

	return "none"
}

func Probe(url string) (error, []ProbeInfo) {
	var cprobes *C.txprobe_t
	rc := C.probe(C.CString(url), (**C.txprobe_t)(unsafe.Pointer(&cprobes)))
	if int(rc) <= 0 {
		return fmt.Errorf("Probing failed"), nil
	}

	probeInfo := make([]ProbeInfo, int(rc))
	probeArray := (*[1 << 10]C.txprobe_t)(unsafe.Pointer(cprobes))
	for i := 0; i < int(rc); i++ {
		probeInfo[i].CodecType = AVMediaType(probeArray[i].codec_type)
		probeInfo[i].CodecID = int(probeArray[i].codec_id)
		probeInfo[i].CodecName = C.GoString((*C.char)(unsafe.Pointer(&probeArray[i].codec_name)))
		probeInfo[i].DurationTs = int(probeArray[i].duration_ts)
		probeInfo[i].TimeBase = big.NewRat(int64(probeArray[i].time_base.num), int64(probeArray[i].time_base.den))
		probeInfo[i].NBFrames = int64(probeArray[i].nb_frames)
		probeInfo[i].StartTime = int64(probeArray[i].start_time)
		if int64(probeArray[i].avg_frame_rate.den) != 0 {
			probeInfo[i].AvgFrameRate = big.NewRat(int64(probeArray[i].avg_frame_rate.num), int64(probeArray[i].avg_frame_rate.den))
		} else {
			probeInfo[i].AvgFrameRate = big.NewRat(int64(probeArray[i].avg_frame_rate.num), int64(1))
		}
		if int64(probeArray[i].frame_rate.den) != 0 {
			probeInfo[i].FrameRate = big.NewRat(int64(probeArray[i].frame_rate.num), int64(probeArray[i].frame_rate.den))
		} else {
			probeInfo[i].FrameRate = big.NewRat(int64(probeArray[i].frame_rate.num), int64(1))
		}
		probeInfo[i].TicksPerFrame = int(probeArray[i].ticks_per_frame)
		probeInfo[i].BitRate = int64(probeArray[i].bit_rate)
		if probeArray[i].has_b_frames > 0 {
			probeInfo[i].Has_B_Frames = true
		} else {
			probeInfo[i].Has_B_Frames = false
		}
		probeInfo[i].Width = int(probeArray[i].width)
		probeInfo[i].Height = int(probeArray[i].height)
		probeInfo[i].PixFmt = int(probeArray[i].pix_fmt)
		if int64(probeArray[i].sample_aspect_ratio.den) != 0 {
			probeInfo[i].SampleAspectRatio = big.NewRat(int64(probeArray[i].sample_aspect_ratio.num), int64(probeArray[i].sample_aspect_ratio.den))
		} else {
			probeInfo[i].SampleAspectRatio = big.NewRat(int64(probeArray[i].sample_aspect_ratio.num), int64(1))
		}
		if int64(probeArray[i].display_aspect_ratio.den) != 0 {
			probeInfo[i].DisplayAspectRatio = big.NewRat(int64(probeArray[i].display_aspect_ratio.num), int64(probeArray[i].display_aspect_ratio.den))
		} else {
			probeInfo[i].DisplayAspectRatio = big.NewRat(int64(probeArray[i].display_aspect_ratio.num), int64(1))
		}
		probeInfo[i].FieldOrder = int(probeArray[i].field_order)
	}

	C.free(unsafe.Pointer(cprobes))

	return nil, probeInfo
}
