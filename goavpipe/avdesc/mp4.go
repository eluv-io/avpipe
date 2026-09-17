package avdesc

import (
	"fmt"

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
