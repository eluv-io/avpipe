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

// #cgo pkg-config: libavcodec libavfilter libavformat libavutil libswresample libavresample libswscale
// #cgo CFLAGS: -I./include
// #include <string.h>
// #include <stdlib.h>
// #include "avpipe_xc.h"
// #include "avpipe.h"
// #include "elv_log.h"
import "C"
import (
	"fmt"
	"math/big"
	"sync"
	"unsafe"

	elog "github.com/eluv-io/log-go"
)

var log = elog.Get("/eluvio/avpipe")

const traceIo bool = false

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
	// MP4Segment 12
	MP4Segment
	// FMP4VideoSegment 13
	FMP4VideoSegment
	// FMP4AudioSegment 14
	FMP4AudioSegment
	// MuxSegment 15
	MuxSegment
	// FrameImage 16
	FrameImage
)

// This is corresponding to AV_NOPTS_VALUE
const AvNoPtsValue = uint64(C.uint64_t(0x8000000000000000))

type XcType int

const (
	XcNone          XcType = iota
	XcVideo                = 1
	XcAudio                = 2
	XcAll                  = 3  // XcAudio | XcVideo
	XcAudioMerge           = 6  // XcAudio | 0x04
	XcAudioJoin            = 10 // XcAudio | 0x08
	XcAudioPan             = 18 // XcAudio | 0x10
	XcMux                  = 32
	XcExtractImages        = 65 // XcVideo | 2^6
)

func XcTypeFromString(txTypeStr string) XcType {
	var txType XcType
	switch txTypeStr {
	case "all":
		txType = XcAll
	case "video":
		txType = XcVideo
	case "audio":
		txType = XcAudio
	case "audio-join":
		txType = XcAudioJoin
	case "audio-merge":
		txType = XcAudioMerge
	case "audio-pan":
		txType = XcAudioPan
	case "mux":
		txType = XcMux
	case "extract-images":
		txType = XcExtractImages
	default:
		txType = XcNone
	}

	return txType
}

type ImageType int

const (
	UnknownImage = iota
	PngImage
	JpgImage
	GifImage
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

const MaxAudioMux = C.MAX_AUDIO_MUX

// XcParams should match with txparams_t in avpipe_xc.h
type XcParams struct {
	Url                    string             `json:"url"`
	BypassTranscoding      bool               `json:"bypass,omitempty"`
	Format                 string             `json:"format,omitempty"`
	StartTimeTs            int64              `json:"start_time_ts,omitempty"`
	SkipOverPts            int64              `json:"skip_over_pts,omitempty"`
	StartPts               int64              `json:"start_pts,omitempty"` // Start PTS for output
	DurationTs             int64              `json:"duration_ts,omitempty"`
	StartSegmentStr        string             `json:"start_segment_str,omitempty"`
	VideoBitrate           int32              `json:"video_bitrate,omitempty"`
	AudioBitrate           int32              `json:"audio_bitrate,omitempty"`
	SampleRate             int32              `json:"sample_rate,omitempty"` // Audio sampling rate
	RcMaxRate              int32              `json:"rc_max_rate,omitempty"`
	RcBufferSize           int32              `json:"rc_buffer_size,omitempty"`
	CrfStr                 string             `json:"crf_str,omitempty"`
	Preset                 string             `json:"preset,omitempty"`
	AudioSegDurationTs     int64              `json:"audio_seg_duration_ts,omitempty"`
	VideoSegDurationTs     int64              `json:"video_seg_duration_ts,omitempty"`
	SegDuration            string             `json:"seg_duration,omitempty"`
	StartFragmentIndex     int32              `json:"start_fragment_index,omitempty"`
	ForceKeyInt            int32              `json:"force_keyint,omitempty"`
	Ecodec                 string             `json:"ecodec,omitempty"`    // Video encoder
	Ecodec2                string             `json:"ecodec2,omitempty"`   // Audio encoder
	Dcodec                 string             `json:"dcodec,omitempty"`    // Video decoder
	Dcodec2                string             `json:"dcodec2,omitempty"`   // Audio decoder
	GPUIndex               int32              `json:"gpu_index,omitempty"` // GPU index if encoder/decoder is GPU (nvidia)
	EncHeight              int32              `json:"enc_height,omitempty"`
	EncWidth               int32              `json:"enc_width,omitempty"`
	CryptIV                string             `json:"crypt_iv,omitempty"`
	CryptKey               string             `json:"crypt_key,omitempty"`
	CryptKID               string             `json:"crypt_kid,omitempty"`
	CryptKeyURL            string             `json:"crypt_key_url,omitempty"`
	CryptScheme            CryptScheme        `json:"crypt_scheme,omitempty"`
	XcType                 XcType             `json:"xc_type,omitempty"`
	Seekable               bool               `json:"seekable,omitempty"`
	WatermarkText          string             `json:"watermark_text,omitempty"`
	WatermarkTimecode      string             `json:"watermark_timecode,omitempty"`
	WatermarkTimecodeRate  float32            `json:"watermark_timecode_rate,omitempty"`
	WatermarkXLoc          string             `json:"watermark_xloc,omitempty"`
	WatermarkYLoc          string             `json:"watermark_yloc,omitempty"`
	WatermarkRelativeSize  float32            `json:"watermark_relative_size,omitempty"`
	WatermarkFontColor     string             `json:"watermark_font_color,omitempty"`
	WatermarkShadow        bool               `json:"watermark_shadow,omitempty"`
	WatermarkShadowColor   string             `json:"watermark_shadow_color,omitempty"`
	WatermarkOverlay       string             `json:"watermark_overlay,omitempty"`      // Buffer containing overlay image
	WatermarkOverlayLen    int                `json:"watermark_overlay_len,omitempty"`  // Length of overlay image
	WatermarkOverlayType   ImageType          `json:"watermark_overlay_type,omitempty"` // Type of overlay image (i.e PngImage, ...)
	StreamId               int32              `json:"stream_id"`                        // Specify stream by ID (instead of index)
	AudioIndex             [MaxAudioMux]int32 `json:"audio_index,omitempty"`
	NumAudio               int32              `json:"n_audio"`
	ChannelLayout          int                `json:"channel_layout"` // Audio channel layout
	MaxCLL                 string             `json:"max_cll,omitempty"`
	MasterDisplay          string             `json:"master_display,omitempty"`
	BitDepth               int32              `json:"bitdepth,omitempty"`
	AudioFillGap           bool               `json:"audio_fill_gap,omitempty"`
	SyncAudioToStreamId    int                `json:"sync_audio_to_stream_id,omitempty"`
	ForceEqualFDuration    bool               `json:"force_equal_frame_duration,omitempty"`
	MuxingSpec             string             `json:"muxing_spec,omitempty"`
	Listen                 bool               `json:"listen"`
	FilterDescriptor       string             `json:"filter_descriptor"`
	SkipDecoding           bool               `json:"skip_decoding"`
	DebugFrameLevel        bool               `json:"debug_frame_level"`
	ExtractImageIntervalTs int64              `json:"extract_image_interval_ts,omitempty"`
	ExtractImagesTs        []int64            `json:"extract_images_ts,omitempty"`
}

// NewXcParams initializes a XcParams struct with unset/default values
func NewXcParams() *XcParams {
	return &XcParams{
		AudioBitrate:           128000,
		AudioSegDurationTs:     -1,
		BitDepth:               8,
		CrfStr:                 "23",
		DurationTs:             -1,
		Ecodec:                 "libx264",
		Ecodec2:                "aac",
		EncHeight:              -1,
		EncWidth:               -1,
		ExtractImageIntervalTs: -1,
		GPUIndex:               -1,
		SampleRate:             -1,
		SegDuration:            "30",
		StartFragmentIndex:     1,
		StartSegmentStr:        "1",
		StreamId:               -1,
		SyncAudioToStreamId:    -1,
		VideoBitrate:           -1,
		VideoSegDurationTs:     -1,
		WatermarkFontColor:     "white",
		WatermarkOverlayType:   JpgImage,
		WatermarkRelativeSize:  0.05,
		WatermarkShadow:        false,
		WatermarkShadowColor:   "black",
		WatermarkTimecodeRate:  -1,
		WatermarkXLoc:          "W*0.05",
		WatermarkYLoc:          "H*0.9",
	}
}

type AVMediaType int

const (
	AVMEDIA_TYPE_UNKNOWN    = -1
	AVMEDIA_TYPE_VIDEO      = 0
	AVMEDIA_TYPE_AUDIO      = 1
	AVMEDIA_TYPE_DATA       = 2 ///< Opaque data information usually continuous
	AVMEDIA_TYPE_SUBTITLE   = 3
	AVMEDIA_TYPE_ATTACHMENT = 4 ///< Opaque data information usually sparse
	AVMEDIA_TYPE_NB         = 5
)

var AVMediaTypeNames = map[AVMediaType]string{
	AVMEDIA_TYPE_UNKNOWN:    "unknown",
	AVMEDIA_TYPE_VIDEO:      "video",
	AVMEDIA_TYPE_AUDIO:      "audio",
	AVMEDIA_TYPE_DATA:       "data",
	AVMEDIA_TYPE_SUBTITLE:   "subtitle",
	AVMEDIA_TYPE_ATTACHMENT: "attachment",
	AVMEDIA_TYPE_NB:         "nb",
}

type AVFieldOrder int

const (
	AV_FIELD_UNKNOWN     = 0
	AV_FIELD_PROGRESSIVE = 1
	AV_FIELD_TT          = 2 //< Top coded_first, top displayed first
	AV_FIELD_BB          = 3 //< Bottom coded first, bottom displayed first
	AV_FIELD_TB          = 4 //< Top coded first, bottom displayed first
	AV_FIELD_BT          = 5 //< Bottom coded first, top displayed first
)

var AVFieldOrderNames = map[AVFieldOrder]string{
	AV_FIELD_UNKNOWN:     "",
	AV_FIELD_PROGRESSIVE: "progressive",
	AV_FIELD_TT:          "tt",
	AV_FIELD_BB:          "bb",
	AV_FIELD_TB:          "tb",
	AV_FIELD_BT:          "bt",
}

type AVStatType int

const (
	AV_IN_STAT_BYTES_READ               = 1
	AV_IN_STAT_AUDIO_FRAME_READ         = 2
	AV_IN_STAT_VIDEO_FRAME_READ         = 4
	AV_IN_STAT_DECODING_AUDIO_START_PTS = 8
	AV_IN_STAT_DECODING_VIDEO_START_PTS = 16
	AV_OUT_STAT_BYTES_WRITTEN           = 32
	AV_OUT_STAT_FRAME_WRITTEN           = 64
	AV_OUT_STAT_ENCODING_END_PTS        = 128
)

type StreamInfo struct {
	StreamIndex        int      `json:"stream_index"`
	StreamId           int32    `json:"stream_id"`
	CodecType          string   `json:"codec_type"`
	CodecID            int      `json:"codec_id,omitempty"`
	CodecName          string   `json:"codec_name,omitempty"`
	DurationTs         int64    `json:"duration_ts,omitempty"`
	TimeBase           *big.Rat `json:"time_base,omitempty"`
	NBFrames           int64    `json:"nb_frames,omitempty"`
	StartTime          int64    `json:"start_time"` // in TS unit
	AvgFrameRate       *big.Rat `json:"avg_frame_rate,omitempty"`
	FrameRate          *big.Rat `json:"frame_rate,omitempty"`
	SampleRate         int      `json:"sample_rate,omitempty"`
	Channels           int      `json:"channels,omitempty"`
	ChannelLayout      int      `json:"channel_layout,omitempty"`
	TicksPerFrame      int      `json:"ticks_per_frame,omitempty"`
	BitRate            int64    `json:"bit_rate,omitempty"`
	Has_B_Frames       bool     `json:"has_b_frame"`
	Width              int      `json:"width,omitempty"`  // Video only
	Height             int      `json:"height,omitempty"` // Video only
	PixFmt             int      `json:"pix_fmt"`          // Video only, it matches with enum AVPixelFormat in FFmpeg
	SampleAspectRatio  *big.Rat `json:"sample_aspect_ratio,omitempty"`
	DisplayAspectRatio *big.Rat `json:"display_aspect_ratio,omitempty"`
	FieldOrder         string   `json:"field_order,omitempty"`
	Profile            int      `json:"profile,omitempty"`
	Level              int      `json:"level,omitempty"`
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
	InStat(avp_stat C.avp_stat_t, stat_args *C.void) error
	OutWriter(fd C.int, buf []byte) (int, error)
	OutSeeker(fd C.int, offset C.int64_t, whence C.int) (int64, error)
	OutCloser(fd C.int) error
	OutStat(avp_stat C.avp_stat_t, stat_args *C.void) error
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

	// Reports some stats
	Stat(statType AVStatType, statArgs interface{}) error
}

type OutputOpener interface {
	// h determines uniquely opening input.
	// fd determines uniquely opening output.
	Open(h, fd int64, stream_index, seg_index int, pts int64, out_type AVType) (OutputHandler, error)
}

type MuxOutputOpener interface {
	// url and fd determines uniquely opening output.
	Open(url string, fd int64, out_type AVType) (OutputHandler, error)
}

type OutputHandler interface {
	// Writes encoded stream to the output.
	Write(buf []byte) (int, error)

	// Seeks to specific offset of the output.
	Seek(offset int64, whence int) (int64, error)

	// Closes the output.
	Close() error

	// Reports some stats
	Stat(avType AVType, statType AVStatType, statArgs interface{}) error
}

// Implement IOHandler
type ioHandler struct {
	input    InputHandler // Input file
	mutex    *sync.Mutex
	outTable map[int64]OutputHandler // Map of integer handle to output interfaces
}

// Global table of handlers
var gHandlers map[int64]*ioHandler = make(map[int64]*ioHandler)
var gMuxHandlers map[int64]OutputHandler = make(map[int64]OutputHandler)
var gURLInputOpeners map[string]InputOpener = make(map[string]InputOpener)             // Keeps InputOpener for specific URL
var gURLOutputOpeners map[string]OutputOpener = make(map[string]OutputOpener)          // Keeps OutputOpener for specific URL
var gURLMuxOutputOpeners map[string]MuxOutputOpener = make(map[string]MuxOutputOpener) // Keeps MuxOutputOpener for specific URL
var gURLOutputOpenersByHandler map[int64]OutputOpener = make(map[int64]OutputOpener)   // Keeps OutputOpener for specific URL
var gHandleNum int64
var gFd int64
var gMutex sync.Mutex
var gInputOpener InputOpener
var gOutputOpener OutputOpener
var gMuxOutputOpener MuxOutputOpener

// This is used to set global input/output opener for avpipe
// If there is no specific input/output opener for a URL, the global
// input/output opener will be used.
func InitIOHandler(inputOpener InputOpener, outputOpener OutputOpener) {
	gInputOpener = inputOpener
	gOutputOpener = outputOpener
}

// Sets the global handlers for muxing (similar to InitIOHandler for transcoding)
func InitMuxIOHandler(inputOpener InputOpener, muxOutputOpener MuxOutputOpener) {
	gInputOpener = inputOpener
	gMuxOutputOpener = muxOutputOpener
}

// This is used to set input/output opener specific to a URL.
// The input/output opener set by this function, is only valid for the URL and will be unset after
// Xc() or Probe() is complete.
func InitUrlIOHandler(url string, inputOpener InputOpener, outputOpener OutputOpener) {
	if inputOpener != nil {
		gMutex.Lock()
		gURLInputOpeners[url] = inputOpener
		gMutex.Unlock()
	}

	if outputOpener != nil {
		gMutex.Lock()
		gURLOutputOpeners[url] = outputOpener
		gMutex.Unlock()
	}
}

// Sets specific IO handler for muxing a url/file (similar to InitUrlIOHandler)
func InitUrlMuxIOHandler(url string, inputOpener InputOpener, muxOutputOpener MuxOutputOpener) {
	if inputOpener != nil {
		gMutex.Lock()
		gURLInputOpeners[url] = inputOpener
		gMutex.Unlock()
	}

	if muxOutputOpener != nil {
		gMutex.Lock()
		gURLMuxOutputOpeners[url] = muxOutputOpener
		gMutex.Unlock()
	}
	log.Debug("InitUrlMuxIOHandler", "url", url, "urlInputOpener", inputOpener == nil, "urlOutputOpener", muxOutputOpener == nil)
}

func getInputOpener(url string) InputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
	if inputOpener, ok := gURLInputOpeners[url]; ok {
		return inputOpener
	}

	return gInputOpener
}

func getOutputOpener(url string) OutputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
	if outputOpener, ok := gURLOutputOpeners[url]; ok {
		return outputOpener
	}

	return gOutputOpener
}

func getMuxOutputOpener(url string) MuxOutputOpener {
	log.Debug("getMuxOutputOpener", "url", url)
	gMutex.Lock()
	defer gMutex.Unlock()
	if muxOutputOpener, ok := gURLMuxOutputOpeners[url]; ok {
		return muxOutputOpener
	}

	return gMuxOutputOpener
}

func putMuxOutputOpener(fd int64, muxOutputHandler OutputHandler) {
	gMutex.Lock()
	gMuxHandlers[fd] = muxOutputHandler
	gMutex.Unlock()
}

func getOutputOpenerByHandler(h int64) OutputOpener {
	gMutex.Lock()
	defer gMutex.Unlock()
	if outputOpener, ok := gURLOutputOpenersByHandler[h]; ok {
		return outputOpener
	}

	return gOutputOpener
}

//export AVPipeOpenInput
func AVPipeOpenInput(url *C.char, size *C.int64_t) C.int64_t {
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	urlInputOpener := getInputOpener(filename)
	urlOutputOpener := getOutputOpener(filename)

	if urlInputOpener == nil || urlOutputOpener == nil {
		log.Error("Input or output opener(s) are not set", "urlInputOpener", urlInputOpener, "urlOutputOpener", urlOutputOpener)
		return C.int64_t(-1)
	}
	log.Debug("AVPipeOpenInput()", "url", filename)

	gMutex.Lock()
	gHandleNum++
	fd := gHandleNum
	gURLOutputOpenersByHandler[fd] = urlOutputOpener
	gMutex.Unlock()

	input, err := urlInputOpener.Open(fd, filename)
	if err != nil {
		return C.int64_t(-1)
	}

	*size = C.int64_t(input.Size())

	h := &ioHandler{input: input, outTable: make(map[int64]OutputHandler), mutex: &sync.Mutex{}}
	log.Debug("AVPipeOpenInput()", "url", filename, "size", *size, "fd", fd)

	gMutex.Lock()
	defer gMutex.Unlock()
	gHandlers[fd] = h
	return C.int64_t(fd)
}

//export AVPipeOpenMuxInput
func AVPipeOpenMuxInput(out_url, url *C.char, size *C.int64_t) C.int64_t {
	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	out_filename := C.GoString((*C.char)(unsafe.Pointer(out_url)))
	urlInputOpener := getInputOpener(out_filename)
	urlOutputOpener := getMuxOutputOpener(out_filename)

	log.Debug("AVPipeOpenMuxInput()", "url", filename, "out_filename", out_filename)

	if urlInputOpener == nil || urlOutputOpener == nil {
		log.Error("Input or output opener(s) are not set", "urlInputOpener", urlInputOpener, "urlOutputOpener", urlOutputOpener)
		return C.int64_t(-1)
	}

	gMutex.Lock()
	gHandleNum++
	fd := gHandleNum
	gMutex.Unlock()

	input, err := urlInputOpener.Open(fd, filename)
	if err != nil {
		return C.int64_t(-1)
	}

	*size = C.int64_t(input.Size())

	h := &ioHandler{input: input, outTable: make(map[int64]OutputHandler), mutex: &sync.Mutex{}}
	log.Debug("AVPipeOpenMuxInput()", "url", filename, "size", *size)

	gMutex.Lock()
	defer gMutex.Unlock()
	gHandlers[fd] = h
	return C.int64_t(fd)
}

//export AVPipeReadInput
func AVPipeReadInput(fd C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	gMutex.Lock()
	h := gHandlers[int64(fd)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

	if traceIo {
		log.Debug("AVPipeReadInput()", "fd", fd, "buf", buf, "sz", sz)
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
		log.Debug("InReader()", "buf_size", len(buf), "n", n, "error", err)
	}
	return n, err
}

//export AVPipeSeekInput
func AVPipeSeekInput(fd C.int64_t, offset C.int64_t, whence C.int) C.int64_t {
	gMutex.Lock()
	h := gHandlers[int64(fd)]
	if h == nil {
		gMutex.Unlock()
		return C.int64_t(-1)
	}
	gMutex.Unlock()
	if traceIo {
		log.Debug("AVPipeSeekInput()", "h", h)
	}

	n, err := h.InSeeker(offset, whence)
	if err != nil {
		return C.int64_t(-1)
	}
	return C.int64_t(n)
}

func (h *ioHandler) InSeeker(offset C.int64_t, whence C.int) (int64, error) {
	n, err := h.input.Seek(int64(offset), int(whence))
	log.Debug("InSeeker()", "offset", offset, "whence", whence, "n", n)
	return n, err
}

//export AVPipeCloseInput
func AVPipeCloseInput(fd C.int64_t) C.int {
	gMutex.Lock()
	h := gHandlers[int64(fd)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	err := h.InCloser()

	// Remove the handler from global table
	delete(gHandlers, int64(fd))
	delete(gURLOutputOpenersByHandler, int64(fd))
	gMutex.Unlock()
	if err != nil {
		return C.int(-1)
	}

	log.Debug("AVPipeCloseInput()", "fd", fd)

	return C.int(0)
}

func (h *ioHandler) InCloser() error {
	err := h.input.Close()
	log.Debug("InCloser()", "error", err)
	return err
}

//export AVPipeStatInput
func AVPipeStatInput(fd C.int64_t, avp_stat C.avp_stat_t, stat_args unsafe.Pointer) C.int {
	gMutex.Lock()
	h := gHandlers[int64(fd)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

	err := h.InStat(avp_stat, stat_args)
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

func (h *ioHandler) InStat(avp_stat C.avp_stat_t, stat_args unsafe.Pointer) error {
	var err error

	switch avp_stat {
	case C.in_stat_bytes_read:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(AV_IN_STAT_BYTES_READ, &statArgs)
	case C.in_stat_decoding_audio_start_pts:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(AV_IN_STAT_DECODING_AUDIO_START_PTS, &statArgs)
	case C.in_stat_decoding_video_start_pts:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(AV_IN_STAT_DECODING_VIDEO_START_PTS, &statArgs)
	case C.in_stat_audio_frame_read:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(AV_IN_STAT_AUDIO_FRAME_READ, &statArgs)
	case C.in_stat_video_frame_read:
		statArgs := *(*uint64)(stat_args)
		err = h.input.Stat(AV_IN_STAT_VIDEO_FRAME_READ, &statArgs)
	}

	return err
}

func (h *ioHandler) putOutTable(fd int64, outHandler OutputHandler) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	if outHandler != nil {
		h.outTable[fd] = outHandler
	} else {
		delete(h.outTable, fd)
	}
}

func (h *ioHandler) getOutTable(fd int64) OutputHandler {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	return h.outTable[fd]
}

func getAVType(av_type C.int) AVType {
	switch av_type {
	case C.avpipe_video_init_stream:
		return DASHVideoInit
	case C.avpipe_audio_init_stream:
		return DASHAudioInit
	case C.avpipe_manifest:
		return DASHManifest
	case C.avpipe_video_segment:
		return DASHVideoSegment
	case C.avpipe_audio_segment:
		return DASHAudioSegment
	case C.avpipe_master_m3u:
		return HLSMasterM3U
	case C.avpipe_video_m3u:
		return HLSVideoM3U
	case C.avpipe_audio_m3u:
		return HLSAudioM3U
	case C.avpipe_aes_128_key:
		return AES128Key
	case C.avpipe_mp4_stream:
		return MP4Stream
	case C.avpipe_fmp4_stream:
		return FMP4Stream
	case C.avpipe_mp4_segment:
		return MP4Segment
	case C.avpipe_video_fmp4_segment:
		return FMP4VideoSegment
	case C.avpipe_audio_fmp4_segment:
		return FMP4AudioSegment
	case C.avpipe_mux_segment:
		return MuxSegment
	case C.avpipe_image:
		return FrameImage
	default:
		return Unknown
	}
}

//export AVPipeOpenOutput
func AVPipeOpenOutput(handler C.int64_t, stream_index, seg_index C.int, pts C.int64_t, stream_type C.int) C.int64_t {

	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int64_t(-1)
	}
	gFd++
	fd := gFd
	gMutex.Unlock()
	out_type := getAVType(stream_type)
	if out_type == Unknown {
		log.Error("AVPipeOpenOutput()", "invalid stream type", stream_type)
		return C.int64_t(-1)
	}

	outputOpener := getOutputOpenerByHandler(int64(handler))
	if outputOpener == nil {
		log.Error("AVPipeOpenOutput() nil outputOpener", "handler", handler)
		return C.int64_t(-1)
	}
	outHandler, err := outputOpener.Open(int64(handler), fd, int(stream_index), int(seg_index), int64(pts), out_type)
	if err != nil {
		log.Error("AVPipeOpenOutput()", "out_type", out_type, "error", err)
		return C.int64_t(-1)
	}

	log.Debug("AVPipeOpenOutput()", "fd", fd, "stream_index", stream_index, "seg_index", seg_index, "pts", pts, "out_type", out_type)
	h.putOutTable(fd, outHandler)

	return C.int64_t(fd)
}

//export AVPipeOpenMuxOutput
func AVPipeOpenMuxOutput(url *C.char, stream_type C.int) C.int64_t {
	var out_type AVType

	gMutex.Lock()
	gFd++
	fd := gFd
	gMutex.Unlock()
	switch stream_type {
	case C.avpipe_mp4_segment:
		out_type = MP4Segment
	case C.avpipe_video_fmp4_segment:
		out_type = FMP4VideoSegment
	case C.avpipe_audio_fmp4_segment:
		out_type = FMP4AudioSegment
	default:
		log.Error("AVPipeOpenOutput()", "invalid stream type", stream_type)
		return C.int64_t(-1)
	}

	filename := C.GoString((*C.char)(unsafe.Pointer(url)))
	muxOutputOpener := getMuxOutputOpener(filename)
	if muxOutputOpener == nil {
		log.Error("AVPipeOpenMuxOutput() nil muxOutputOpener", "url", filename)
		return C.int64_t(-1)
	}
	outHandler, err := muxOutputOpener.Open(filename, fd, out_type)
	if err != nil {
		log.Error("AVPipeOpenOutput()", "out_type", out_type, "error", err)
		return C.int64_t(-1)
	}

	log.Debug("AVPipeOpenOutput()", "fd", fd, "out_type", out_type)
	putMuxOutputOpener(fd, outHandler)

	return C.int64_t(fd)
}

//export AVPipeWriteOutput
func AVPipeWriteOutput(handler C.int64_t, fd C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	if sz <= 0 {
		return C.int(0)
	}

	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()
	if traceIo {
		log.Debug("AVPipeWriteOutput", "fd", fd, "sz", sz)
	}

	if h.getOutTable(int64(fd)) == nil {
		msg := fmt.Sprintf("OutWriterX outTable entry is NULL, fd=%d", fd)
		panic(msg)
	}

	//gobuf := C.GoBytes(unsafe.Pointer(buf), sz)
	// This should be the equivalent of using GoBytes() but safer if the
	// Go implementation uses C pointer to wrap a slice.
	gobuf := make([]byte, sz)
	C.memcpy(unsafe.Pointer(&gobuf[0]), unsafe.Pointer(buf), C.size_t(sz))

	n, err := h.OutWriter(fd, gobuf)
	if err != nil {
		return C.int(-1)
	}

	return C.int(n)
}

//export AVPipeWriteMuxOutput
func AVPipeWriteMuxOutput(fd C.int64_t, buf *C.uint8_t, sz C.int) C.int {
	if traceIo {
		log.Debug("AVPipeWriteMuxOutput", "fd", fd, "sz", sz)
	}

	gMutex.Lock()
	outHandler := gMuxHandlers[int64(fd)]
	if outHandler == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

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
		log.Debug("OutWriter written", "n", n, "error", err)
	}
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

//export AVPipeSeekMuxOutput
func AVPipeSeekMuxOutput(fd C.int64_t, offset C.int64_t, whence C.int) C.int {
	gMutex.Lock()
	outHandler := gMuxHandlers[int64(fd)]
	if outHandler == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

	n, err := outHandler.Seek(int64(offset), int(whence))
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
	defer h.putOutTable(int64(fd), nil)
	err := h.OutCloser(fd)
	if err != nil {
		return C.int(-1)
	}

	log.Debug("AVPipeCloseOutput()", "fd", fd)

	return C.int(0)
}

//export AVPipeCloseMuxOutput
func AVPipeCloseMuxOutput(fd C.int64_t) C.int {
	gMutex.Lock()
	outHandler := gMuxHandlers[int64(fd)]
	if outHandler == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

	err := outHandler.Close()
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

//export AVPipeStatOutput
func AVPipeStatOutput(handler C.int64_t,
	fd C.int64_t,
	buf_type C.avpipe_buftype_t,
	avp_stat C.avp_stat_t,
	stat_args unsafe.Pointer) C.int {

	gMutex.Lock()
	h := gHandlers[int64(handler)]
	if h == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

	err := h.OutStat(fd, buf_type, avp_stat, stat_args)
	if err != nil {
		return C.int(-1)
	}

	return C.int(0)
}

//export AVPipeStatMuxOutput
func AVPipeStatMuxOutput(fd C.int64_t, avp_stat C.avp_stat_t, stat_args unsafe.Pointer) C.int {
	gMutex.Lock()
	outHandler := gMuxHandlers[int64(fd)]
	if outHandler == nil {
		gMutex.Unlock()
		return C.int(-1)
	}
	gMutex.Unlock()

	var err error
	switch avp_stat {
	case C.out_stat_bytes_written:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(MuxSegment, AV_OUT_STAT_BYTES_WRITTEN, &statArgs)
	case C.out_stat_encoding_end_pts:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(MuxSegment, AV_OUT_STAT_ENCODING_END_PTS, &statArgs)
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
	av_type C.avpipe_buftype_t,
	avp_stat C.avp_stat_t,
	stat_args unsafe.Pointer) error {

	var err error
	outHandler := h.getOutTable(int64(fd))
	if outHandler == nil {
		return fmt.Errorf("OutStat nil handler, fd=%d", int64(fd))
	}

	avType := getAVType(C.int(av_type))
	switch avp_stat {
	case C.out_stat_bytes_written:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(avType, AV_OUT_STAT_BYTES_WRITTEN, &statArgs)
	case C.out_stat_encoding_end_pts:
		statArgs := *(*uint64)(stat_args)
		err = outHandler.Stat(avType, AV_OUT_STAT_ENCODING_END_PTS, &statArgs)
	case C.out_stat_frame_written:
		encodingFramesStats := (*C.encoding_frame_stats_t)(stat_args)
		statArgs := &EncodingFrameStats{
			TotalFramesWritten: int64(encodingFramesStats.total_frames_written),
			FramesWritten:      int64(encodingFramesStats.frames_written),
		}
		err = outHandler.Stat(avType, AV_OUT_STAT_FRAME_WRITTEN, statArgs)
	}

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
func Version() string {
	return C.GoString((*C.char)(unsafe.Pointer(C.avpipe_version())))
}

func getCParams(params *XcParams) (*C.xcparams_t, error) {
	extractImagesSize := len(params.ExtractImagesTs)

	// same field order as avpipe_xc.h
	cparams := &C.xcparams_t{
		url:                       C.CString(params.Url),
		format:                    C.CString(params.Format),
		start_time_ts:             C.int64_t(params.StartTimeTs),
		skip_over_pts:             C.int64_t(params.SkipOverPts),
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
		n_audio:                   C.int(params.NumAudio),
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
		filter_descriptor:         C.CString(params.FilterDescriptor),
		skip_decoding:             C.int(0),
		extract_image_interval_ts: C.int64_t(params.ExtractImageIntervalTs),
		extract_images_sz:         C.int(extractImagesSize),

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

	if params.AudioFillGap {
		cparams.audio_fill_gap = C.int(1)
	}

	if params.SkipDecoding {
		cparams.skip_decoding = C.int(1)
	}

	if params.Listen {
		cparams.listen = C.int(1)
	}

	if params.NumAudio > MaxAudioMux {
		return nil, fmt.Errorf("Invalid number of audio streams NumAudio=%d", params.NumAudio)
	}

	if params.DebugFrameLevel {
		cparams.debug_frame_level = C.int(1)
	}

	for i := 0; i < int(params.NumAudio); i++ {
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

// params: transcoding parameters
func Xc(params *XcParams) error {

	// Convert XcParams to C.txparams_t
	if params == nil {
		log.Error("Failed transcoding, params are not set.")
		return EAV_PARAM
	}

	cparams, err := getCParams(params)
	if err != nil {
		log.Error("Transcoding failed", err, "url", params.Url)
	}

	rc := C.xc((*C.xcparams_t)(unsafe.Pointer(cparams)))

	gMutex.Lock()
	defer gMutex.Unlock()
	delete(gURLInputOpeners, params.Url)
	delete(gURLOutputOpeners, params.Url)

	return avpipeError(rc)
}

func Mux(params *XcParams) error {
	if params == nil {
		log.Error("Failed muxing, params are not set")
		return EAV_PARAM
	}

	cparams, err := getCParams(params)
	if err != nil {
		log.Error("Muxing failed", err, "url", params.Url)
	}

	rc := C.mux((*C.xcparams_t)(unsafe.Pointer(cparams)))

	gMutex.Lock()
	defer gMutex.Unlock()
	delete(gURLInputOpeners, params.Url)
	delete(gURLOutputOpeners, params.Url)

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

func Probe(url string, seekable bool) (*ProbeInfo, error) {
	var cprobe *C.xcprobe_t
	var cseekable C.int
	var n_streams C.int

	if seekable {
		cseekable = C.int(1)
	} else {
		cseekable = C.int(0)
	}

	rc := C.probe(C.CString(url), cseekable, (**C.xcprobe_t)(unsafe.Pointer(&cprobe)), (*C.int)(unsafe.Pointer(&n_streams)))
	if int(rc) != 0 {
		return nil, avpipeError(rc)
	}

	probeInfo := &ProbeInfo{}
	probeInfo.StreamInfo = make([]StreamInfo, int(n_streams))
	probeArray := (*[1 << 10]C.stream_info_t)(unsafe.Pointer(cprobe.stream_info))
	for i := 0; i < int(n_streams); i++ {
		probeInfo.StreamInfo[i].StreamIndex = int(probeArray[i].stream_index)
		probeInfo.StreamInfo[i].StreamId = int32(probeArray[i].stream_id)
		probeInfo.StreamInfo[i].CodecType = AVMediaTypeNames[AVMediaType(probeArray[i].codec_type)]
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
		probeInfo.StreamInfo[i].FieldOrder = AVFieldOrderNames[AVFieldOrder(probeArray[i].field_order)]
		probeInfo.StreamInfo[i].Profile = int(probeArray[i].profile)
		probeInfo.StreamInfo[i].Level = int(probeArray[i].level)
	}

	probeInfo.ContainerInfo.FormatName = C.GoString((*C.char)(unsafe.Pointer(cprobe.container_info.format_name)))
	probeInfo.ContainerInfo.Duration = float64(cprobe.container_info.duration)

	C.free(unsafe.Pointer(cprobe.stream_info))
	C.free(unsafe.Pointer(cprobe))

	gMutex.Lock()
	defer gMutex.Unlock()
	delete(gURLInputOpeners, url)
	delete(gURLOutputOpeners, url)

	return probeInfo, nil
}

// Returns a handle and error (if there is any error)
// In case of error the handle would be zero
func XcInit(params *XcParams) (int32, error) {
	// Convert XcParams to C.txparams_t
	if params == nil {
		log.Error("Failed transcoding, params are not set.")
		return -1, EAV_PARAM
	}

	cparams, err := getCParams(params)
	if err != nil {
		log.Error("Initializing transcoder failed", err, "url", params.Url)
	}

	var handle C.int32_t
	rc := C.xc_init((*C.xcparams_t)(unsafe.Pointer(cparams)), (*C.int32_t)(unsafe.Pointer(&handle)))
	if rc != C.eav_success {
		return -1, avpipeError(rc)
	}

	return int32(handle), nil
}

func XcRun(handle int32) error {
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
	for i, _ := range a {
		a[i].StreamIndex = i
		a[i].CodecType = AVMediaTypeNames[AVMediaType(AVMEDIA_TYPE_UNKNOWN)]
	}
	for _, v := range s {
		a[v.StreamIndex] = v
	}
	return a
}
