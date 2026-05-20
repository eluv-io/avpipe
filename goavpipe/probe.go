package goavpipe

import "math/big"

// ProbeInfo is the top-level result returned by avpipe.Probe. It mirrors the
// structure of ffprobe's JSON output, with a "format" object and a "streams"
// array.
type ProbeInfo struct {
	ContainerInfo ContainerInfo `json:"format"`
	StreamInfo    []StreamInfo  `json:"streams"`
}

// ContainerInfo holds container-level metadata for a probed media file.
type ContainerInfo struct {
	// FormatName is the comma-separated list of short format names as reported
	// by FFmpeg (e.g. "mov,mp4,m4a,3gp,3g2,mj2").
	FormatName string `json:"format_name"`
	// Duration is the total container duration in seconds.
	Duration float64 `json:"duration"`
}

// StreamInfo describes a single audio or video stream within a media container,
// with fields roughly in the order they appear in ffprobe's JSON output.
// Not all ffprobe fields are present.
type StreamInfo struct {
	// StreamIndex is the zero-based index of this stream within the container.
	StreamIndex int `json:"stream_index"` // ffprobe: "index"

	// CodecName is the short codec name as reported by FFmpeg (e.g. "h264", "eac3").
	CodecName string `json:"codec_name,omitempty"`

	// Profile is the codec profile IDC. ProfileName holds the human-readable form.
	// 0 (omitted) when FFmpeg reports AV_PROFILE_UNKNOWN.
	Profile int `json:"profile,omitempty"` // ffprobe: string value

	// ProfileName is the human-readable codec profile (e.g. "High", "Main").
	// Populated by avpipe.Probe() via GetProfileName().
	ProfileName string `json:"profile_name,omitempty"` // ffprobe: "profile"

	// CodecType is the media type: "audio", "video", "data", etc.
	CodecType string `json:"codec_type,omitempty"`

	// CodecTagString is the four-character code (4CC) from the container
	// (e.g. "avc1", "ec-3"), derived from AVCodecParameters.codec_tag via
	// av_fourcc_make_string().
	CodecTagString string `json:"codec_tag_string,omitempty"`

	// CodecID is the FFmpeg AVCodecID integer for this stream's codec.
	CodecID int `json:"codec_id,omitempty"` // no ffprobe equivalent

	// MimeCodecString is the RFC 6381 codec string suitable for use in MIME
	// type codecs parameters (e.g. "avc1.4d4028", "ec-3").
	// TODO: Populate this via av_mime_codec_str() once the FFmpeg dependency is
	//       updated to 8.1+ (libavformat 62.7+). As a workaround, use
	//       mp4e.ExtractCodecInfo(), which parses the MP4 box layer directly.
	MimeCodecString string `json:"mime_codec_string,omitempty"`

	// Video-specific fields

	// Width is the coded frame width in pixels.
	Width int `json:"width,omitempty"`

	// Height is the coded frame height in pixels.
	Height int `json:"height,omitempty"`

	// HasBFrames indicates whether the stream contains B-frames.
	HasBFrames bool `json:"has_b_frames,omitempty"`

	// SampleAspectRatio is the sample (pixel) aspect ratio.
	SampleAspectRatio *big.Rat `json:"sample_aspect_ratio,omitempty"`

	// DisplayAspectRatio is the display aspect ratio.
	DisplayAspectRatio *big.Rat `json:"display_aspect_ratio,omitempty"`

	// PixFmt is the FFmpeg pixel format (enum AVPixelFormat). Nil for
	// non-video streams. 0 means AV_PIX_FMT_YUV420P.
	PixFmt *int `json:"pix_fmt,omitempty"` // ffprobe: string value

	// Level is the codec level IDC (e.g. 40 = H.264 level 4.0).
	// 0 (omitted) when FFmpeg reports AV_LEVEL_UNKNOWN.
	Level int `json:"level,omitempty"`

	// ColorRange is the luma/chroma sample range: "tv" (limited, 16–235) or
	// "pc" (full, 0–255). "unknown" when unspecified by the stream.
	ColorRange string `json:"color_range,omitempty"`

	// ColorSpace is the YCbCr matrix coefficients used to derive luma/chroma
	// from RGB (e.g. "bt709", "bt2020nc"). "unknown" when unspecified.
	ColorSpace string `json:"color_space,omitempty"`

	// ColorTransfer is the opto-electrical transfer characteristic (gamma
	// curve), e.g. "bt709", "smpte2084" (PQ/HDR10), "arib-std-b67" (HLG).
	// "unknown" when unspecified.
	ColorTransfer string `json:"color_transfer,omitempty"`

	// ColorPrimaries identifies the chromaticity coordinates of the color
	// primaries (e.g. "bt709", "bt2020"). "unknown" when unspecified.
	ColorPrimaries string `json:"color_primaries,omitempty"`

	// FieldOrder describes the interlacing of the video (e.g. "progressive",
	// "tt", "bb"). Empty for non-video streams or when unknown.
	FieldOrder string `json:"field_order,omitempty"`

	// Audio-specific fields

	// SampleRate is the audio sampling rate in Hz.
	SampleRate int `json:"sample_rate,omitempty"`

	// Channels is the number of audio channels.
	Channels int `json:"channels,omitempty"`

	// ChannelLayout is the FFmpeg channel layout bitmask (AV_CH_LAYOUT_*).
	// ChannelLayoutName holds the human-readable form.
	ChannelLayout int `json:"channel_layout,omitempty"` // ffprobe: string value

	// ChannelLayoutName is the human-readable channel layout (e.g. "5.1(side)").
	// Populated by avpipe.Probe() via ChannelLayoutName().
	ChannelLayoutName string `json:"channel_layout_name,omitempty"` // ffprobe: "channel_layout"

	// Stream/container fields

	// StreamId is the format-specific stream identifier from the container
	// (e.g. the PID in MPEG-TS, or the track ID in MP4).
	StreamId int `json:"stream_id"` // ffprobe: "id"

	// FrameRate is the real base frame rate of the stream. For variable frame
	// rate streams this is the lowest common denominator.
	FrameRate *big.Rat `json:"frame_rate,omitempty"` // ffprobe: "r_frame_rate"

	// AvgFrameRate is the average frame rate, as stored in the container.
	AvgFrameRate *big.Rat `json:"avg_frame_rate,omitempty"`

	// TimeBase is the fundamental unit of time for this stream's timestamps,
	// expressed as a rational number (e.g. "1/48000" for 48 kHz audio).
	TimeBase *big.Rat `json:"time_base"`

	// StartTime is the presentation timestamp of the first packet, in TimeBase units.
	StartTime int64 `json:"start_time"` // ffprobe: "start_pts"

	// DurationTs is the total stream duration in TimeBase units.
	DurationTs int64 `json:"duration_ts,omitempty"`

	// BitRate is the average bit rate in bits per second.
	BitRate int64 `json:"bit_rate,omitempty"`

	// NBFrames is the total number of frames in the stream.
	NBFrames int64 `json:"nb_frames,omitempty"`

	// Tags contains container-level metadata key/value pairs for this stream
	// (e.g. "language": "eng").
	Tags map[string]string `json:"tags,omitempty"`

	// SideData holds auxiliary data attached to the stream, such as a display
	// matrix for rotation. Each element is typed (e.g. SideDataDisplayMatrix).
	SideData []interface{} `json:"side_data,omitempty"` // ffprobe: "side_data_list"

	// Stereo3DType is the stereoscopic 3D layout of the video stream
	// (e.g. "side by side", "top and bottom"). Empty for non-stereoscopic
	// streams. Derived from AV_PKT_DATA_STEREO3D side data.
	Stereo3DType string `json:"stereo3d_type,omitempty"`

	// MasteringDisplay is the SMPTE ST 2086 mastering display color volume,
	// formatted as an x265-compatible string with red/green/blue primaries
	// (x/y coordinates) and min/max luminance
	// (e.g. "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)").
	// Non-empty only for HDR streams. Derived from
	// AV_PKT_DATA_MASTERING_DISPLAY_METADATA side data.
	MasteringDisplay string `json:"mastering_display,omitempty"`

	// MaxCLL is the SMPTE ST 2086 content light level, formatted as
	// "MaxCLL=N,MaxFALL=N" where MaxCLL is the maximum content light level
	// and MaxFALL is the maximum frame-average light level, both in nits
	// (e.g. "MaxCLL=1000,MaxFALL=400"). Non-empty only for HDR streams.
	// Derived from AV_PKT_DATA_CONTENT_LIGHT_LEVEL side data.
	MaxCLL string `json:"max_cll,omitempty"`

	//  The following ffprobe fields are not populated (yet), but may be of
	//  interest:
	//
	//   disposition          object  stream flags: default, forced, hearing_impaired, multilayer, etc.
	//   bits_per_raw_sample  int     bit depth per sample (e.g. 8, 10, 12); critical for HDR/10-bit detection
	//   coded_width          int     coded frame width before cropping; may differ from Width when padding is present
	//   coded_height         int     coded frame height before cropping
	//   sample_fmt           string  audio sample format (e.g. "fltp", "s16")
	//   duration             float64 stream duration in seconds (we only expose DurationTs in timebase units)
	//   initial_padding      int     audio encoder delay in samples; needed for gapless playback
	//   codec_long_name      string  human-readable codec description (e.g. "H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10")
	//   chroma_location      string  chroma sample position (e.g. "left")
	//   bits_per_sample      int     audio PCM bit depth; relevant for uncompressed/lossless audio (PCM, FLAC)
}

// SideDataDisplayMatrix holds the display transformation matrix side data
// attached to a video stream. It is the Go representation of
// AV_PKT_DATA_DISPLAYMATRIX / AV_FRAME_DATA_DISPLAYMATRIX.
type SideDataDisplayMatrix struct {
	// Type is always "Display Matrix".
	Type string `json:"side_data_type"`
	// Rotation is the counter-clockwise rotation angle in degrees.
	Rotation float64 `json:"rotation"`
	// RotationCw is the clockwise rotation angle in degrees (negation of Rotation).
	RotationCw float64 `json:"rotation_cw"`
}

// StreamInfoAsArray builds an array where each stream is at its corresponding
// index by filling in non-existing index positions with codec type "unknown".
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
		a[i].CodecType = AVMediaTypeNames[AVMediaType(AVMEDIA_TYPE_UNKNOWN)]
	}
	for _, v := range s {
		a[v.StreamIndex] = v
	}
	return a
}
