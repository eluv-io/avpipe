package avdesc

import (
	"fmt"
	"math/big"

	"github.com/eluv-io/avpipe/goavpipe/util"
)

// VideoLayout describes how a video stream encodes one or more views.
//
// Numeric values match libavpipe video_layout_t, so XcParams.VideoLayout - an
// int32 carrying the same values rather than this type - converts either way.
// CodecInfo.VideoLayout is typed.
type VideoLayout int

const (
	VideoLayoutMono   VideoLayout = 0
	VideoLayoutSbs    VideoLayout = 3  // frame-packed side-by-side
	VideoLayoutTb     VideoLayout = 4  // frame-packed top-bottom
	VideoLayoutMVHEVC VideoLayout = 10 // multi-layer HEVC (MV-HEVC)
)

func (l VideoLayout) String() string {
	switch l {
	case VideoLayoutMono:
		return "mono"
	case VideoLayoutSbs:
		return "sbs"
	case VideoLayoutTb:
		return "tb"
	case VideoLayoutMVHEVC:
		return "mvhevc"
	}
	return fmt.Sprintf("unknown(%d)", int(l))
}

// MediaType is the kind of media a track carries. The values match the strings
// goavpipe reports as StreamInfo.CodecType, so the two are comparable without
// translation - avdesc cannot use goavpipe's AVMediaType, since goavpipe
// depends on this package.
type MediaType string

const (
	MediaTypeVideo MediaType = "video"
	MediaTypeAudio MediaType = "audio"
)

// CodecInfo identifies the codec of one MP4 track, as described by its sample
// description entry (the "stsd" box) and the codec configuration boxes beneath
// it. It says what the bytes are, not how the track is laid out.
//
// It lives here, next to EC3Info and DOVIInfo, because it crosses the same
// boundary they do: mp4e parses it out of the box layer, and goavpipe reports
// it as part of a probe result. Keeping one type means neither side converts.
// Note this package must stay free of mp4ff - the shape belongs here, the
// parsing stays in mp4e.
//
// Derived names for the numeric fields - ProfileName, LevelName - are not
// carried here. They are a rendering choice, they need the codec tables in
// mp4e, and a type that crosses a wire boundary should not decide how it is
// displayed.
type CodecInfo struct {
	// CodecTagString is the sample description entry 4-character code (in the
	// MP4 "stsd" box) as registered by the MP4RA; e.g. "hvc1", "avc1", "ec-3"
	CodecTagString string `json:"codec_tag_string,omitempty"`

	// MimeCodecString is the RFC 6381 codec string for use in MIME type codecs
	// parameters (e.g. "hvc1.2.4.L120.90", "avc1.640028", "mp4a.40.2").
	MimeCodecString string `json:"mime_codec_string,omitempty"`

	// ProfileIDC is the codec profile IDC. Each codec defines the value separately;
	// e.g. 2 = HEVC Main 10, 100 = AVC High
	ProfileIDC int `json:"profile_idc,omitempty"`

	// Level is the codec level IDC. Each codec defines the value separately:
	//  * Divide by 30 for HEVC, e.g. 120 → 4.0
	//  * Divide by 10 for AVC, e.g. 40  → 4.0
	Level int `json:"level,omitempty"`

	// Channels is the number of audio channels. For E-AC-3 this comes from the
	// dec3 box rather than the sample entry, which is often wrong there.
	Channels int `json:"channels,omitempty"`

	// EC3 is set only when the codec is ec-3
	EC3 *EC3Info `json:"ec3,omitempty"`

	// DOVI is set only when the codec entry contains a Dolby Vision configuration
	// box (dvcC, dvvC, or dvwC). Common cases:
	//   hvc1/hev1 with dvvC child — Profile 8.x (cross-compatible)
	//   dvh1/dvhe with dvcC child — Profile 5 or Profile 20 (standalone DV)
	// DOVI.BoxType records which of dvcC/dvvC/dvwC was found.
	DOVI *DOVIInfo `json:"dovi,omitempty"`

	// VideoLayout describes the stereoscopic layout (mono, sbs, mvhevc).
	VideoLayout VideoLayout `json:"video_layout,omitempty"`

	// EnhancementProfileIDC is the enhancement-layer general_profile_idc.
	// Only meaningful for VideoLayout == VideoLayoutMVHEVC.
	EnhancementProfileIDC int `json:"enhancement_profile_idc,omitempty"`
}

func (c CodecInfo) String() string { return util.JSONString(c) }

// MP4TrackInfo describes one track of an MP4 container: its codec, plus the
// layout facts carried by the track's own boxes.
//
// CodecInfo answers "what are these bytes"; the fields added here answer "how
// is this track laid out", which the sample description entry and the media
// header carry and which a caller otherwise has to take from configuration.
//
// Accuracy is not uniform, and the differences matter:
//
//   - Neither channel count is right on its own; call AudioChannels() rather
//     than reading either field directly. CodecInfo.Channels comes from a codec
//     configuration box and is empty for a codec the parser does not decode,
//     while ChannelCount is the raw sample-entry value and is wrong for some
//     codecs that do get decoded.
//
//   - SampleRate is the integer part of a 16.16 fixed-point field truncated to
//     16 bits, so it cannot express a rate above 65535 Hz, and for HE-AAC the
//     sample entry commonly carries half the real rate (the esds
//     AudioSpecificConfig is authoritative there).
//
//   - Width and Height are *coded* dimensions. A pasp box can make the display
//     dimensions differ.
//
//   - FrameRate is not read from a box at all - see the field.
type MP4TrackInfo struct {
	CodecInfo

	// TrackID is the tkhd track identifier. It is the container's own name for
	// the track, which makes it the join key against goavpipe's
	// StreamInfo.StreamId - unlike a position in a slice, which only matches
	// while both sides enumerate tracks the same way. int, not the box's
	// uint32, so that join needs no conversion.
	TrackID int `json:"track_id,omitempty"`

	// MediaType is the sample entry's kind: video or audio. Tracks of any
	// other kind are not described.
	MediaType MediaType `json:"media_type,omitempty"`

	// Width and Height are the coded dimensions from a visual sample entry.
	Width  int `json:"width,omitempty"`
	Height int `json:"height,omitempty"`

	// SampleRate is the audio sampling rate in Hz from an audio sample entry.
	SampleRate int `json:"sample_rate,omitempty"`

	// ChannelCount is the raw audio sample-entry channel count. Prefer
	// AudioChannels() over reading this or CodecInfo.Channels directly.
	ChannelCount int `json:"channel_count,omitempty"`

	// Timescale is the track's media timescale (mdhd), the unit its timestamps
	// are expressed in.
	Timescale int `json:"timescale,omitempty"`

	// Language is the mdhd ISO-639-2/T code. Note avpipe does not propagate a
	// source stream's language to its output, so a track avpipe produced
	// reports "und" whatever the source carried.
	Language string `json:"language,omitempty"`

	// FrameRate is the video frame rate, nil when it could not be derived.
	//
	// Unlike every other field here it is derived rather than read: no box
	// carries a frame rate, so it comes from sample durations, which live past
	// the moov. An init segment on its own therefore yields nil. A rate is
	// reported as the standard broadcast rate only when the durations rule out
	// every other one; otherwise it is the measurement. mp4e.resolveFrameRate
	// has the derivation and why it is a bound rather than a nearest match.
	FrameRate *big.Rat `json:"frame_rate,omitempty"`
}

// MP4MovInfo describes an MP4 container and its tracks.
//
// Fragmented reports whether the movie box declares fragments (mvex). With the
// brands, it is how a caller confirms that a buffer it cut at a box boundary is
// a usable init segment before trusting anything parsed out of it.
type MP4MovInfo struct {
	// MajorBrand and CompatibleBrands come from ftyp.
	MajorBrand       string   `json:"major_brand,omitempty"`
	CompatibleBrands []string `json:"compatible_brands,omitempty"`

	// Timescale is the movie timescale (mvhd).
	Timescale int `json:"timescale,omitempty"`

	// DurationTs is the movie duration in Timescale units. It is 0 for a
	// fragmented file, where the duration lives in the fragments.
	DurationTs int64 `json:"duration_ts,omitempty"`

	// Fragmented reports whether moov declares an mvex box.
	Fragmented bool `json:"fragmented,omitempty"`

	// Tracks are the video and audio tracks, in moov order. Tracks of other
	// handler types are omitted.
	Tracks []*MP4TrackInfo `json:"tracks,omitempty"`
}

// AudioChannels returns the track's channel count, 0 for video.
//
// Two boxes carry a channel count and neither is reliable alone, so this
// prefers the codec configuration box and falls back to the sample entry:
//
//	codec   CodecInfo.Channels        ChannelCount (sample entry)
//	ec-3    6  from dec3, correct     2  wrong - ec-3 entries carry a stub
//	mp4a    2  copied from the entry  2
//	ac-4    0  not decoded            2 / 6 / 10, correct
//
// The ec-3 row is why a caller cannot just read the sample entry, and the ac-4
// row is why it cannot just read CodecInfo.Channels: a codec the parser does
// not decode leaves that field empty, and the sample entry is then the only
// source there is. Both rows are real - measured against the ac-4 and
// Dolby Digital Plus JOC assets in media/, checked against ffprobe.
//
// Zero means no channel count was found at all, which for an audio track means
// a malformed sample entry.
func (t MP4TrackInfo) AudioChannels() int {
	if t.Channels > 0 {
		return t.Channels
	}
	return t.ChannelCount
}

func (t MP4TrackInfo) String() string { return util.JSONString(t) }
func (m MP4MovInfo) String() string   { return util.JSONString(m) }
