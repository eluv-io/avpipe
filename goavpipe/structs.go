package goavpipe

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
)

type AVStatType int

const (
	AV_IN_STAT_BYTES_READ               = 1
	AV_IN_STAT_AUDIO_FRAME_READ         = 2
	AV_IN_STAT_VIDEO_FRAME_READ         = 3
	AV_IN_STAT_DECODING_AUDIO_START_PTS = 4
	AV_IN_STAT_DECODING_VIDEO_START_PTS = 5
	AV_OUT_STAT_BYTES_WRITTEN           = 6
	AV_OUT_STAT_FRAME_WRITTEN           = 7
	AV_IN_STAT_FIRST_KEYFRAME_PTS       = 8
	AV_OUT_STAT_ENCODING_END_PTS        = 9
	AV_OUT_STAT_START_FILE              = 10
	AV_OUT_STAT_END_FILE                = 11
	AV_IN_STAT_DATA_SCTE35              = 12
	AV_IN_STAT_MPEGTS                   = 13
)

func (a AVStatType) Name() string {
	switch a {
	case AV_IN_STAT_BYTES_READ:
		return "AV_IN_STAT_BYTES_READ"
	case AV_IN_STAT_AUDIO_FRAME_READ:
		return "AV_IN_STAT_AUDIO_FRAME_READ"
	case AV_IN_STAT_VIDEO_FRAME_READ:
		return "AV_IN_STAT_VIDEO_FRAME_READ"
	case AV_IN_STAT_DECODING_AUDIO_START_PTS:
		return "AV_IN_STAT_DECODING_AUDIO_START_PTS"
	case AV_IN_STAT_DECODING_VIDEO_START_PTS:
		return "AV_IN_STAT_DECODING_VIDEO_START_PTS"
	case AV_IN_STAT_FIRST_KEYFRAME_PTS:
		return "AV_IN_STAT_FIRST_KEYFRAME_PTS"
	case AV_OUT_STAT_BYTES_WRITTEN:
		return "AV_OUT_STAT_BYTES_WRITTEN"
	case AV_OUT_STAT_FRAME_WRITTEN:
		return "AV_OUT_STAT_FRAME_WRITTEN"
	case AV_OUT_STAT_ENCODING_END_PTS:
		return "AV_OUT_STAT_ENCODING_END_PTS"
	case AV_OUT_STAT_START_FILE:
		return "AV_OUT_STAT_START_FILE"
	case AV_OUT_STAT_END_FILE:
		return "AV_OUT_STAT_END_FILE"
	case AV_IN_STAT_DATA_SCTE35:
		return "AV_IN_STAT_DATA_SCTE35"
	case AV_IN_STAT_MPEGTS:
		return "AV_IN_STAT_MPEGTS"
	default:
		return fmt.Sprintf("Unknown(%d)", a)
	}

}

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
	// MpegtsSegment 17
	MpegtsSegment
)

func (a AVType) Name() string {
	switch a {
	case DASHManifest:
		return "DASHManifest"
	case DASHVideoInit:
		return "DASHVideoInit"
	case DASHVideoSegment:
		return "DASHVideoSegment"
	case DASHAudioInit:
		return "DASHAudioInit"
	case DASHAudioSegment:
		return "DASHAudioSegment"
	case HLSMasterM3U:
		return "HLSMasterM3U"
	case HLSVideoM3U:
		return "HLSVideoM3U"
	case HLSAudioM3U:
		return "HLSAudioM3U"
	case AES128Key:
		return "AES128Key"
	case MP4Stream:
		return "MP4Stream"
	case FMP4Stream:
		return "FMP4Stream"
	case MP4Segment:
		return "MP4Segment"
	case FMP4VideoSegment:
		return "FMP4VideoSegment"
	case FMP4AudioSegment:
		return "FMP4AudioSegment"
	case MuxSegment:
		return "MuxSegment"
	case FrameImage:
		return "FrameImage"
	case MpegtsSegment:
		return "MpegtsSegment"
	default:
		return fmt.Sprintf("Unknown(%d)", a)
	}
}

type AVClass = string

var AVClassE = struct {
	Mez      AVClass
	Abr      AVClass
	Manifest AVClass
	Mux      AVClass
	Frame    AVClass
	Unknown  AVClass
}{
	Mez:      "mez",
	Abr:      "abr",
	Manifest: "manifest",
	Mux:      "mux",
	Frame:    "frame",
	Unknown:  "unknown",
}

func (a AVType) AVClass() AVClass {
	switch a {
	case FMP4AudioSegment, FMP4VideoSegment, MP4Segment:
		return AVClassE.Mez
	case DASHAudioInit, DASHAudioSegment, DASHVideoInit, DASHVideoSegment:
		return AVClassE.Abr
	case HLSAudioM3U, HLSMasterM3U, HLSVideoM3U, DASHManifest:
		return AVClassE.Manifest
	case FrameImage:
		return AVClassE.Frame
	case MuxSegment, MP4Stream, FMP4Stream:
		return AVClassE.Mux
	default:
		return AVClassE.Unknown
	}
}

// This is corresponding to AV_NOPTS_VALUE
const AvNoPtsValue = uint64(0x8000000000000000)

type XcType int

const (
	XcNone             XcType = iota
	XcVideo            XcType = 1
	XcAudio            XcType = 2
	XcAll              XcType = 3  // XcAudio | XcVideo
	XcAudioMerge       XcType = 6  // XcAudio | 0x04
	XcAudioJoin        XcType = 10 // XcAudio | 0x08
	XcAudioPan         XcType = 18 // XcAudio | 0x10
	XcMux              XcType = 32
	XcExtractImages    XcType = 65  // XcVideo | 2^6
	XcExtractAllImages XcType = 129 // XcVideo | 2^7
	Xcprobe            XcType = 256
)

type XcProfile int

const (
	XcProfileNone         XcProfile = iota
	XcProfileH264BaseLine           = 66  // C.FF_PROFILE_H264_BASELINE
	XcProfileH264Heigh              = 100 // C.FF_PROFILE_H264_HIGH
	XcProfileH264Heigh10            = 110 // C.FF_PROFILE_H264_HIGH_10
)

func XcTypeFromString(xcTypeStr string) XcType {
	var xcType XcType
	switch xcTypeStr {
	case "all":
		xcType = XcAll
	case "video":
		xcType = XcVideo
	case "audio":
		xcType = XcAudio
	case "audio-join":
		xcType = XcAudioJoin
	case "audio-merge":
		xcType = XcAudioMerge
	case "audio-pan":
		xcType = XcAudioPan
	case "mux":
		xcType = XcMux
	case "extract-images":
		xcType = XcExtractImages
	case "extract-all-images":
		xcType = XcExtractAllImages
	default:
		xcType = XcNone
	}

	return xcType
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

// XcParams should match with txparams_t in avpipe_xc.h
type XcParams struct {
	Url                    string      `json:"url"`
	InputCfg               InputConfig `json:"input_cfg"`
	BypassTranscoding      bool        `json:"bypass,omitempty"`
	Format                 string      `json:"format,omitempty"`
	StartTimeTs            int64       `json:"start_time_ts,omitempty"`
	StartPts               int64       `json:"start_pts,omitempty"` // Start PTS for output
	DurationTs             int64       `json:"duration_ts,omitempty"`
	StartSegmentStr        string      `json:"start_segment_str,omitempty"`
	VideoBitrate           int32       `json:"video_bitrate,omitempty"`
	AudioBitrate           int32       `json:"audio_bitrate,omitempty"`
	SampleRate             int32       `json:"sample_rate,omitempty"` // Audio sampling rate
	RcMaxRate              int32       `json:"rc_max_rate,omitempty"`
	RcBufferSize           int32       `json:"rc_buffer_size,omitempty"`
	CrfStr                 string      `json:"crf_str,omitempty"`
	Preset                 string      `json:"preset,omitempty"`
	AudioSegDurationTs     int64       `json:"audio_seg_duration_ts,omitempty"`
	VideoSegDurationTs     int64       `json:"video_seg_duration_ts,omitempty"`
	SegDuration            string      `json:"seg_duration,omitempty"`
	StartFragmentIndex     int32       `json:"start_fragment_index,omitempty"`
	ForceKeyInt            int32       `json:"force_keyint,omitempty"`
	Ecodec                 string      `json:"ecodec,omitempty"`    // Video encoder
	Ecodec2                string      `json:"ecodec2,omitempty"`   // Audio encoder
	Dcodec                 string      `json:"dcodec,omitempty"`    // Video decoder
	Dcodec2                string      `json:"dcodec2,omitempty"`   // Audio decoder
	GPUIndex               int32       `json:"gpu_index,omitempty"` // GPU index if encoder/decoder is GPU (nvidia)
	EncHeight              int32       `json:"enc_height,omitempty"`
	EncWidth               int32       `json:"enc_width,omitempty"`
	CryptIV                string      `json:"crypt_iv,omitempty"`
	CryptKey               string      `json:"crypt_key,omitempty"`
	CryptKID               string      `json:"crypt_kid,omitempty"`
	CryptKeyURL            string      `json:"crypt_key_url,omitempty"`
	CryptScheme            CryptScheme `json:"crypt_scheme,omitempty"`
	XcType                 XcType      `json:"xc_type,omitempty"`
	Seekable               bool        `json:"seekable,omitempty"`
	WatermarkText          string      `json:"watermark_text,omitempty"`
	WatermarkTimecode      string      `json:"watermark_timecode,omitempty"`
	WatermarkTimecodeRate  float32     `json:"watermark_timecode_rate,omitempty"`
	WatermarkXLoc          string      `json:"watermark_xloc,omitempty"`
	WatermarkYLoc          string      `json:"watermark_yloc,omitempty"`
	WatermarkRelativeSize  float32     `json:"watermark_relative_size,omitempty"`
	WatermarkFontColor     string      `json:"watermark_font_color,omitempty"`
	WatermarkShadow        bool        `json:"watermark_shadow,omitempty"`
	WatermarkShadowColor   string      `json:"watermark_shadow_color,omitempty"`
	WatermarkOverlay       string      `json:"watermark_overlay,omitempty"`      // Buffer containing overlay image
	WatermarkOverlayLen    int         `json:"watermark_overlay_len,omitempty"`  // Length of overlay image
	WatermarkOverlayType   ImageType   `json:"watermark_overlay_type,omitempty"` // Type of overlay image (i.e PngImage, ...)
	StreamId               int32       `json:"stream_id"`                        // Specify stream by ID (instead of index)
	AudioIndex             []int32     `json:"audio_index"`                      // the length of this is equal to the number of audios
	ChannelLayout          int         `json:"channel_layout"`                   // Audio channel layout
	MaxCLL                 string      `json:"max_cll,omitempty"`
	MasterDisplay          string      `json:"master_display,omitempty"`
	BitDepth               int32       `json:"bitdepth,omitempty"`
	SyncAudioToStreamId    int         `json:"sync_audio_to_stream_id"`
	ForceEqualFDuration    bool        `json:"force_equal_frame_duration,omitempty"`
	MuxingSpec             string      `json:"muxing_spec,omitempty"`
	Listen                 bool        `json:"listen"`
	ConnectionTimeout      int         `json:"connection_timeout"`
	FilterDescriptor       string      `json:"filter_descriptor"`
	SkipDecoding           bool        `json:"skip_decoding"`
	DebugFrameLevel        bool        `json:"debug_frame_level"`
	ExtractImageIntervalTs int64       `json:"extract_image_interval_ts,omitempty"`
	ExtractImagesTs        []int64     `json:"extract_images_ts,omitempty"`
	VideoTimeBase          int         `json:"video_time_base,omitempty"`
	VideoFrameDurationTs   int         `json:"video_frame_duration_ts,omitempty"`
	Rotate                 int         `json:"rotate,omitempty"`
	Profile                string      `json:"profile,omitempty"`
	Level                  int         `json:"level,omitempty"`
	Deinterlace            int         `json:"deinterlace,omitempty"`
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

// Custom unmarshalJSON for XcParams to make things backwards compatible with prior serialization
//
// Explanations of backwards compatible serializations:
//  1. NEW: The number of audios is specified by the length of the `AudioIndex` slice.
//     OLD: The number of audios was specified by a larger `AudioIndex` array and a `n_audio` field specifying the number.
//     CONVERSION: If a `n_audio` field exists, the `AudioIndex` slice is shortened to be that length.
func (p *XcParams) UnmarshalJSON(data []byte) error {
	// The alias does not have the problematic unmarshal JSON that makes embedding XcParams into xcParamsDecoder bad
	type xcpAlias XcParams

	type xcParamsDecoder struct {
		xcpAlias
		NumAudio int32 `json:"n_audio"`
	}

	var xcpd xcParamsDecoder
	xcpd.xcpAlias = xcpAlias(*p)
	if err := json.Unmarshal(data, &xcpd); err != nil {
		return err
	}

	*p = XcParams(xcpd.xcpAlias)

	if xcpd.NumAudio != 0 && len(p.AudioIndex) > int(xcpd.NumAudio) {
		p.AudioIndex = p.AudioIndex[:xcpd.NumAudio]
	}

	return nil
}

func (p *XcParams) UnmarshalMap(m map[string]interface{}) error {
	// Pass through JSON unmarshalling for centralization of unmarshalling
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return p.UnmarshalJSON(b)
}

func (p *XcParams) Validate() error {
	// TODO: Fill this out more completely
	// For frontend visibility, if there are multiple independent config sections that
	// have errors, we should probably concatenate them into an error list

	inputErr := p.InputCfg.Validate(p.Url)
	if inputErr != nil {
		return inputErr
	}

	return nil
}

type CopyMode string

const (
	CopyModeUnknown CopyMode = ""        // Default - no copy of the input
	CopyModeNone    CopyMode = "none"    // Don't create a copy of the input
	CopyModeRaw     CopyMode = "raw"     // Create a verbatim copy of the input
	CopyModeRemuxed CopyMode = "remuxed" // Demux and remux the input
	// CopyModeRepackage can be used to repackage RTMP to MPEGTS in combination
	// with raw_ts packaging and a URL starting with RTMP (example for future use)
	CopyModeRepackage CopyMode = "repackage"
	// CopyModeRetranscode can be used to replace an elementary stream in the MPEGTS
	// such as converting jpegxs to h264, then remuxing (example for future use)
	CopyModeRetranscode CopyMode = "retranscode_stream"
)

type InputConfig struct {
	CopyMode      CopyMode                  `json:"copy_mode"`
	CopyPackaging transport.TsPackagingMode `json:"copy_packaging"`
	// NOTE: Even if not bypassing libav reader, UDP will bypass the libav reader
	BypassLibavReader bool `json:"bypass_libav_reader"`
}

func (ic *InputConfig) Validate(url string) error {
	// For backwards compatibility reasons, the zero input config is valid
	zeroInputConfig := InputConfig{}
	if *ic == zeroInputConfig {
		return nil
	}

	isRTP := strings.HasPrefix(url, "rtp://")
	isSRT := strings.HasPrefix(url, "srt://")
	isUDP := strings.HasPrefix(url, "udp://")

	useLibavReader := !ic.BypassLibavReader

	if useLibavReader && !(isRTP || isSRT || isUDP) {
		return errors.New("FFMPEG reader can only be used with RTP, SRT, or UDP URLs")
	}

	switch ic.CopyMode {
	case CopyModeUnknown:
		return errors.New("copy mode must be set to a valid value")
	case CopyModeNone, CopyModeRaw, CopyModeRemuxed:
	case CopyModeRepackage, CopyModeRetranscode:
		return fmt.Errorf("copy mode not implemented: %s", ic.CopyMode)
	default:
		return fmt.Errorf("invalid copy mode: %s", ic.CopyMode)
	}

	switch ic.CopyMode {
	case CopyModeNone:
		if ic.CopyPackaging != transport.UnknownPackagingMode {
			return errors.New("copy packaging cannot be set if copy mode is 'none'")
		}
		return nil
	case CopyModeRaw:
		if useLibavReader {
			return errors.New("libav reader cannot be used with raw copy mode")
		} else if ic.CopyPackaging == transport.UnknownPackagingMode {
			return errors.New("copy packaging must be set if copy mode is set")
		} else if ic.CopyPackaging == transport.RtpTs && !isRTP {
			// TODO: We should be able to handle RTP over UDP as well
			return errors.New("RTP packaging can only be used with RTP URLs")
		}
		return nil
	case CopyModeRemuxed:
		if ic.CopyPackaging != transport.RawTs {
			return errors.New("remuxed copying only supports raw ts packaging")
		}
		return nil
	}

	return nil
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
