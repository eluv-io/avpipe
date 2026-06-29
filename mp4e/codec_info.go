package mp4e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	mp4bits "github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/Eyevinn/mp4ff/sei"

	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/eluv-io/errors-go"
)

// Mp4VideoLayout describes the view layout detected from MP4 sample-entry
// boxes and codec configuration metadata.
type Mp4VideoLayout int

const (
	Mp4VideoLayoutMono   Mp4VideoLayout = 0
	Mp4VideoLayoutSbs    Mp4VideoLayout = 3
	Mp4VideoLayoutTb     Mp4VideoLayout = 4
	Mp4VideoLayoutMVHEVC Mp4VideoLayout = 10
)

// CodecInfo contains information about a media stream's codecs
type CodecInfo struct {
	// CodecTagString is the sample description entry 4-character code (in the
	// MP4 "stsd" box) as registered by the MP4RA; e.g. "hvc1", "avc1", "ec-3"
	CodecTagString string `json:"codec_tag_string"`

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

	// Channels is the number of audio channels.
	Channels int `json:"channels,omitempty"`

	// EC3 is set only when the codec is ec-3
	EC3 *avdesc.EC3Info `json:"ec3,omitempty"`

	// DOVI is set only when the codec entry contains a Dolby Vision configuration
	// box (dvcC, dvvC, or dvwC). Common cases:
	//   hvc1/hev1 with dvvC child — Profile 8.x (cross-compatible)
	//   dvh1/dvhe with dvcC child — Profile 5 or Profile 20 (standalone DV)
	// DOVI.BoxType records which of dvcC/dvvC/dvwC was found.
	DOVI *avdesc.DOVIInfo `json:"dovi,omitempty"`

	// VideoLayout describes the stereoscopic layout (mono, sbs, mvhevc).
	VideoLayout Mp4VideoLayout `json:"video_layout,omitempty"`

	// EnhancementProfileIDC is the enhancement-layer general_profile_idc.
	// Only meaningful for VideoLayout == Mp4VideoLayoutMVHEVC.
	EnhancementProfileIDC int `json:"enhancement_profile_idc,omitempty"`

	// AVC holds H.264 SPS/PPS parameters (and, when media samples are present,
	// a summary of actual slice reference usage). Populated only for avc1/avc3.
	AVC *AVCParams `json:"avc,omitempty"`
}

// AVCParams holds the H.264 SPS/PPS parameters that must be consistent across
// segments sharing a common init segment (and across a splice) for seamless
// playback, plus an optional summary of the coded slices' actual reference
// usage. The classic failure is the slices referencing more frames than the
// SPS max_num_ref_frames declares ("reference count overflow") — see
// AVCSliceStats.ExceedsMaxRefFrames.
type AVCParams struct {
	// --- SPS ---
	MaxNumRefFrames  uint `json:"max_num_ref_frames"` // the "ref" count (x264 --ref)
	PicOrderCntType  uint `json:"pic_order_cnt_type"`
	FrameMbsOnlyFlag bool `json:"frame_mbs_only_flag"`
	Width            uint `json:"width,omitempty"`
	Height           uint `json:"height,omitempty"`

	// --- PPS ---
	EntropyCodingCABAC   bool `json:"entropy_coding_cabac"` // true = CABAC, false = CAVLC
	Transform8x8Mode     bool `json:"transform_8x8_mode"`   // x264 8x8dct
	WeightedPred         bool `json:"weighted_pred"`        // x264 weightp
	WeightedBipredIDC    uint `json:"weighted_bipred_idc"`
	ConstrainedIntraPred bool `json:"constrained_intra_pred"`
	NumRefIdxL0Default   uint `json:"num_ref_idx_l0_default_active"` // PPS default (minus1 + 1)
	NumRefIdxL1Default   uint `json:"num_ref_idx_l1_default_active"`

	// Slices summarizes per-slice reference usage parsed from the media
	// samples. Present only when the input contains media fragments.
	Slices *AVCSliceStats `json:"slices,omitempty"`

	// Parameter sets retained for slice-header parsing (not serialized).
	spsMap map[uint32]*avc.SPS
	ppsMap map[uint32]*avc.PPS
}

// AVCSliceStats summarizes the actual reference usage of the coded slices.
// A mismatch between this and the SPS (max_num_ref_frames) means the segment
// is internally inconsistent and will fail to decode across an init change or
// splice — exactly the corruption seen in mis-encoded partial segments.
type AVCSliceStats struct {
	Count             int            `json:"count"`
	SliceTypeCounts   map[string]int `json:"slice_type_counts"`            // I/P/B/SP/SI -> count
	NumRefIdxL0Counts map[int]int    `json:"num_ref_idx_l0_active_counts"` // active L0 refs -> #slices
	// MaxNumRefIdxL0Active is the largest L0 reference-list size declared by any
	// slice header. NOTE: this is informational only — it is NOT a decodability
	// signal. A list may legitimately be larger than max_num_ref_frames (the
	// decoder pads by repeating refs), and real reference corruption lives in
	// the entropy-coded macroblock ref_idx values / DPB state, which header
	// parsing cannot see. Use an actual decode to verify decodability.
	MaxNumRefIdxL0Active int `json:"max_num_ref_idx_l0_active"`

	// UnparsedSlices is the number of coded slices whose header could not be
	// parsed against the active SPS/PPS — e.g. the slice references a parameter
	// set id not present in the init, or the SPS bit-layout doesn't match. A
	// non-zero value when checking a media segment against a supplied/foreign
	// init is a strong signal that the m4s does NOT match that init.
	UnparsedSlices int `json:"unparsed_slices,omitempty"`

	// --- B-frame info (only present when the stream contains B-slices) ---

	// HasBFrames is true if any coded slice is a B-slice.
	HasBFrames bool `json:"has_b_frames,omitempty"`
	// MaxConsecutiveBFrames is the longest run of consecutive B-slices in
	// decode order (e.g. 1 for IbBP, 2 for IBBP, larger for hierarchical B).
	MaxConsecutiveBFrames int `json:"max_consecutive_b_frames,omitempty"`
	// NumRefIdxL1Counts is the histogram of active L1 (forward) references per
	// B-slice; MaxNumRefIdxL1Active is the maximum. L1 is used only by B-slices.
	NumRefIdxL1Counts    map[int]int `json:"num_ref_idx_l1_active_counts,omitempty"`
	MaxNumRefIdxL1Active int         `json:"max_num_ref_idx_l1_active,omitempty"`
	// MaxReorderDepthFrames is the actual B-frame reference span: the maximum
	// (decode_index - display_index) over all frames, i.e. how many frames the
	// decoder must hold because B-frames reference frames decoded later. It is
	// derived from each slice's POC vs decode order (0 when no reordering).
	MaxReorderDepthFrames int `json:"max_reorder_depth_frames,omitempty"`

	// X264 reports encoder settings found in user-data-unregistered SEI NALUs
	// carried by the media. This is useful when checking a media fragment
	// against a foreign init, because the fragment may have no in-band SPS/PPS.
	X264 *AVCX264Settings `json:"x264,omitempty"`

	// CompatibilityWarnings records likely init/media mismatches detected from
	// slice headers and encoder settings. These are warnings because a full
	// decoder is still the final arbiter for entropy-coded macroblock state.
	CompatibilityWarnings []string `json:"compatibility_warnings,omitempty"`
}

// AVCX264Settings summarizes the x264 "options:" user-data SEI when present.
type AVCX264Settings struct {
	Raw              string `json:"raw"`
	Ref              int    `json:"ref,omitempty"`
	CABAC            *bool  `json:"cabac,omitempty"`
	Transform8x8     *bool  `json:"transform_8x8,omitempty"`
	WeightP          *int   `json:"weightp,omitempty"`
	BFrames          *int   `json:"bframes,omitempty"`
	KeyInt           int    `json:"keyint,omitempty"`
	Stitchable       *bool  `json:"stitchable,omitempty"`
	ConstrainedIntra *bool  `json:"constrained_intra,omitempty"`
}

// MarshalJSON adds additional profile_name and level_name fields alongside the
// numeric profile_idc and level_idc
func (c CodecInfo) MarshalJSON() ([]byte, error) {
	type alias CodecInfo
	return json.Marshal(&struct {
		alias
		ProfileName string `json:"profile_name,omitempty"`
		LevelName   string `json:"level_name,omitempty"`
	}{
		alias:       alias(c),
		ProfileName: ProfileName(c.CodecTagString, c.ProfileIDC),
		LevelName:   LevelName(c.CodecTagString, c.Level),
	})
}

// ExtractCodecInfo decodes an MP4 container and returns codec information for
// all tracks. Supports AVC (avc1/avc3) and HEVC (hvc1/hev1) video tracks, and
// E-AC-3 (ec-3) and AAC (mp4a) audio tracks.
//
// The entire file is read into memory, including the mdat payload. When the
// caller only needs init-level fields (codec strings, profile/level, channels,
// EC3/JOC, Dolby Vision, video layout) and not the AVC slice statistics, prefer
// ExtractCodecInfoLazy, which skips the mdat payload via seeking.
func ExtractCodecInfo(r io.Reader) (infos []*CodecInfo, err error) {
	const op = "mp4e.ExtractCodecInfo"
	e := errors.T(op, errors.K.Invalid.Default())

	mp4Data, err := mp4.DecodeFile(r)
	if err != nil {
		return nil, e("reason", "failed to parse MP4", "cause", sanitizeString(err.Error()))
	}
	return extractCodecInfoFromFile(mp4Data, true)
}

// ExtractCodecInfoLazy returns the same init-level codec information as
// ExtractCodecInfo WITHOUT reading the mdat payload into memory: mp4ff's lazy
// mdat decode mode reads only the box headers and the moov box and seeks past
// the (potentially gigabyte-sized) media payload. Peak memory is therefore
// ~the size of moov, independent of the file or segment size, and it works
// regardless of where moov sits (front for faststart, back otherwise).
//
// Because the media samples are not loaded, the AVC slice statistics
// (CodecInfo.AVC.Slices) are NOT populated. This is the mode used by Probe,
// which consumes only init-level fields. Requires an io.ReadSeeker.
func ExtractCodecInfoLazy(r io.ReadSeeker) (infos []*CodecInfo, err error) {
	const op = "mp4e.ExtractCodecInfoLazy"
	e := errors.T(op, errors.K.Invalid.Default())

	mp4Data, err := mp4.DecodeFile(r, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, e("reason", "failed to parse MP4", "cause", sanitizeString(err.Error()))
	}
	return extractCodecInfoFromFile(mp4Data, false)
}

// extractCodecInfoFromFile builds CodecInfo for every track of a decoded MP4.
// When analyzeSlices is true and the input carries media fragments, the AVC
// track is augmented with the actual slice reference-usage statistics parsed
// from the media samples — which requires the mdat payload to be in memory
// (i.e. a non-lazy decode). With analyzeSlices false the mdat-dependent work is
// skipped, so a lazily decoded file (no mdat in memory) can be passed safely.
func extractCodecInfoFromFile(mp4Data *mp4.File, analyzeSlices bool) (infos []*CodecInfo, err error) {
	e := errors.T("mp4e.extractCodecInfoFromFile", errors.K.Invalid.Default())

	// Fragmented MP4 (fMP4 init segment): moov is under mp4Data.Init.
	// Regular MP4: moov is directly under mp4Data.
	var moov *mp4.MoovBox
	if mp4Data.Init != nil {
		moov = mp4Data.Init.Moov
	} else if mp4Data.Moov != nil {
		moov = mp4Data.Moov
	} else if len(mp4Data.Segments) > 0 {
		// Bare media segment (moof+mdat, no init/moov). Slice analysis needs the
		// media samples; skip it entirely on a lazy decode where mdat was not
		// read (there is no init-level codec info to report in this case).
		if !analyzeSlices {
			return nil, nil
		}
		// Analyze the slices and pull SPS/PPS from in-band NAL units if present
		// (usually absent in DASH — they live in the init; pass one via
		// ExtractCodecInfoWithInit).
		return extractFromMediaSegments(mp4Data.Segments, nil), nil
	} else {
		return nil, e("reason", "moov box not found")
	}

	for _, trak := range moov.Traks {
		if trak.Mdia == nil || trak.Mdia.Minf == nil ||
			trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil ||
			len(trak.Mdia.Minf.Stbl.Stsd.Children) == 0 {
			continue
		}
		se := trak.Mdia.Minf.Stbl.Stsd.Children[0]

		var info *CodecInfo
		if vse, ok := se.(*mp4.VisualSampleEntryBox); ok {
			info, err = parseVisualSampleEntryBox(vse)
		} else if ase, ok := se.(*mp4.AudioSampleEntryBox); ok {
			info, err = parseAudioSampleEntryBox(ase)
		} else {
			continue
		}

		if err != nil {
			return nil, e("reason", "failed to parse MP4 sample entry box", err)
		} else if info != nil {
			infos = append(infos, info)
		}
	}

	// If the input carries media fragments (a self-contained segment), parse
	// the coded slices and attach the actual reference-usage summary to the
	// AVC track. (Assumes a single AVC video track, which is the case for the
	// per-segment files this is used on.) Skipped on a lazy decode, where the
	// media samples were not read into memory.
	if analyzeSlices && len(mp4Data.Segments) > 0 {
		for _, info := range infos {
			if info.AVC == nil || info.AVC.spsMap == nil {
				continue
			}
			if stats := analyzeAVCSlices(mp4Data.Segments, info.AVC.spsMap,
				info.AVC.ppsMap); stats != nil && stats.Count > 0 {
				info.AVC.Slices = stats
			}
		}
	}

	return
}

// ExtractCodecInfoWithInit pairs a media segment with an init segment WITHOUT
// concatenating them, and parses the media's coded slices against the init's
// SPS/PPS. This is the way to check whether a DASH media segment actually
// matches a given init: if the m4s was produced by a different encode, its
// slices will not parse cleanly against this init and Slices.UnparsedSlices
// will be non-zero (and the SPS/PPS reported are the init's, for comparison).
func ExtractCodecInfoWithInit(media, initSeg io.Reader) ([]*CodecInfo, error) {
	const op = "mp4e.ExtractCodecInfoWithInit"
	e := errors.T(op, errors.K.Invalid.Default())

	initInfos, err := ExtractCodecInfo(initSeg)
	if err != nil {
		return nil, e("reason", "failed to parse init segment", err)
	}
	var avcInfo *CodecInfo
	for _, ci := range initInfos {
		if ci.AVC != nil && ci.AVC.spsMap != nil {
			avcInfo = ci
			break
		}
	}
	if avcInfo == nil {
		return nil, e("reason", "init segment has no AVC video track with parameter sets")
	}

	mediaData, err := mp4.DecodeFile(media)
	if err != nil {
		return nil, e("reason", "failed to parse media segment", "cause", sanitizeString(err.Error()))
	}
	if len(mediaData.Segments) == 0 {
		return nil, e("reason", "media input has no fragments (expected a media segment)")
	}

	// Slice stats parsed against the INIT's parameter sets — the whole point.
	avcInfo.AVC.Slices = analyzeAVCSlices(mediaData.Segments, avcInfo.AVC.spsMap, avcInfo.AVC.ppsMap)
	return []*CodecInfo{avcInfo}, nil
}

// extractFromMediaSegments builds CodecInfo for a bare media segment (no init).
// SPS/PPS come from initParams if supplied, else from in-band NAL units (if
// any). When no parameter sets are available the slice headers cannot be fully
// parsed (only slice types), which is surfaced via Slices.UnparsedSlices.
func extractFromMediaSegments(segments []*mp4.MediaSegment, initParams *AVCParams) []*CodecInfo {
	params := initParams
	if params == nil {
		params = scanInbandAVCParams(segments) // nil if none in-band
	}

	info := &CodecInfo{CodecTagString: "avc1"}
	var spsMap map[uint32]*avc.SPS
	var ppsMap map[uint32]*avc.PPS
	if params != nil {
		info.AVC = params
		spsMap, ppsMap = params.spsMap, params.ppsMap
		for _, s := range spsMap { // representative SPS for codec string/profile
			info.MimeCodecString = avc.CodecString("avc1", s)
			info.ProfileIDC = int(s.Profile)
			info.Level = int(s.Level)
			break
		}
	} else {
		info.AVC = &AVCParams{}
	}
	info.AVC.Slices = analyzeAVCSlices(segments, spsMap, ppsMap)
	return []*CodecInfo{info}
}

// scanInbandAVCParams collects any SPS/PPS NAL units carried in-band in the
// media samples and parses them. Returns nil if none are present (the common
// DASH case, where parameter sets live only in the init segment).
func scanInbandAVCParams(segments []*mp4.MediaSegment) *AVCParams {
	var spsNalus, ppsNalus [][]byte
	for _, seg := range segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(nil)
			if err != nil {
				continue
			}
			for i := range samples {
				nalus, err := avc.GetNalusFromSample(samples[i].Data)
				if err != nil {
					continue
				}
				for _, nalu := range nalus {
					if len(nalu) == 0 {
						continue
					}
					switch avc.GetNaluType(nalu[0]) {
					case avc.NALU_SPS:
						spsNalus = append(spsNalus, nalu)
					case avc.NALU_PPS:
						ppsNalus = append(ppsNalus, nalu)
					}
				}
			}
			if len(spsNalus) > 0 && len(ppsNalus) > 0 {
				break
			}
		}
	}
	if len(spsNalus) == 0 {
		return nil
	}
	params, err := parseAVCParams(spsNalus, ppsNalus)
	if err != nil {
		return nil
	}
	return params
}

func parseAudioSampleEntryBox(se *mp4.AudioSampleEntryBox) (*CodecInfo, error) {
	switch se.Type() {
	case "ec-3":
		return parseEC3CodecInfo(se)
	case "mp4a":
		return parseMP4ACodecInfo(se)
	default:
		return &CodecInfo{CodecTagString: se.Type()}, nil
	}
}

func parseEC3CodecInfo(se *mp4.AudioSampleEntryBox) (*CodecInfo, error) {
	e := errors.T("parseEC3CodecInfo", errors.K.Invalid.Default())
	if se.Dec3 == nil {
		return nil, e("reason", "ec3 sample entry box missing dec3 box")
	}
	d := se.Dec3
	nChannels, chanMap := d.ChannelInfo()

	// Parse the JOC extension from the dec3 reserved bytes (ETSI TS 103 420).
	// Layout (big-endian, MSB-first within each byte):
	//   bits[7:1]  reserved (7 bits, all zero)
	//   bits[0]    flag_ec3_extension_type_a (1 bit, LSB of first byte)
	//   bits[15:8] complexity_index_type_a   (8 bits, second byte; present iff flag==1)
	var joc bool
	var complexityIndex int
	if len(d.Reserved) >= 1 && d.Reserved[0]&0x01 == 1 {
		joc = true
		if len(d.Reserved) >= 2 {
			complexityIndex = int(d.Reserved[1])
		}
	}

	return &CodecInfo{
		MimeCodecString: "ec-3",
		CodecTagString:  "ec-3",
		Channels:        nChannels,
		EC3: &avdesc.EC3Info{
			ChanMap:         chanMap,
			JOC:             joc,
			ComplexityIndex: complexityIndex,
		},
	}, nil
}

func parseMP4ACodecInfo(se *mp4.AudioSampleEntryBox) (*CodecInfo, error) {
	e := errors.T("parseMP4ACodecInfo", errors.K.Invalid.Default())
	if se.Esds == nil {
		return nil, e("reason", "esds box missing")
	}
	dc := se.Esds.DecConfigDescriptor
	if dc == nil || dc.DecSpecificInfo == nil {
		return nil, e("reason", "decoder config or specific info missing from esds box")
	}
	asc, err := aac.DecodeAudioSpecificConfig(bytes.NewReader(dc.DecSpecificInfo.DecConfig))
	if err != nil {
		return nil, e("reason", "DecodeAudioSpecificConfig failed", err)
	}
	return &CodecInfo{
		// RFC 6381: mp4a.40.{AOT} where 0x40 = MPEG-4 Audio objectTypeIndication
		MimeCodecString: fmt.Sprintf("mp4a.40.%d", asc.ObjectType),
		CodecTagString:  "mp4a",
		ProfileIDC:      int(asc.ObjectType),
		Channels:        int(se.ChannelCount),
	}, nil
}

// ParseDOVIBox parses the payload of a dvcC, dvvC, or dvwC box and returns a DOVIInfo.
// Layout (from FFmpeg dovi_isom.c):
//
//	byte 0:   dv_version_major
//	byte 1:   dv_version_minor
//	byte 2-3 (big-endian uint16):
//	  bits 15-9: dv_profile  (7 bits)
//	  bits  8-3: dv_level    (6 bits)
//	  bit     2: rpu_present (1 bit)
//	  bit     1: el_present  (1 bit)
//	  bit     0: bl_present  (1 bit)
//	byte 4:
//	  bits 7-4: dv_bl_signal_compatibility_id (4 bits)
func ParseDOVIBox(payload []byte) (*avdesc.DOVIInfo, error) {
	const op = "mp4e.ParseDOVIBox"
	e := errors.Template(op, errors.K.Invalid.Default())
	if len(payload) < 5 {
		return nil, e("reason", "payload too short", "len", len(payload))
	}
	word := uint16(payload[2])<<8 | uint16(payload[3])
	return &avdesc.DOVIInfo{
		VersionMajor:            int(payload[0]),
		VersionMinor:            int(payload[1]),
		Profile:                 int((word >> 9) & 0x7f),
		Level:                   int((word >> 3) & 0x3f),
		RPUPresent:              (word>>2)&1 == 1,
		ELPresent:               (word>>1)&1 == 1,
		BLPresent:               word&1 == 1,
		BLSignalCompatibilityID: int((payload[4] >> 4) & 0x0f),
	}, nil
}

// sanitizeString replaces invalid UTF-8 sequences and non-printable characters
// with '?' so error messages containing binary data are safe to log.
func sanitizeString(s string) string {
	s = strings.ToValidUTF8(s, "?")
	return strings.Map(func(r rune) rune {
		if r >= 32 && utf8.ValidRune(r) {
			return r
		}
		return '?'
	}, s)
}

func IsDOVIBoxType(boxType string) bool {
	return boxType == "dvvC" || boxType == "dvcC" || boxType == "dvwC"
}

func parseVisualSampleEntryBox(se *mp4.VisualSampleEntryBox) (*CodecInfo, error) {
	codecTag := se.Type()
	e := errors.T("parseVisualSampleEntryBox", errors.K.Invalid, "codecTag", codecTag)

	switch codecTag {
	case "hvc1", "hev1", "dvh1", "dvhe":
		if se.HvcC == nil {
			return nil, e("reason", "hvcC box missing")
		}
		var sps *hevc.SPS
		for _, arr := range se.HvcC.NaluArrays {
			if arr.NaluType() != hevc.NALU_SPS || len(arr.Nalus) == 0 {
				continue
			}
			parsed, err := hevc.ParseSPSNALUnit(arr.Nalus[0])
			if err != nil {
				return nil, e(err, "reason", "failed to parse HEVC SPS")
			}
			sps = parsed
			break
		}
		if sps == nil {
			return nil, e("reason", "no SPS found in hvcC")
		}

		info := &CodecInfo{
			MimeCodecString: hevc.CodecString(codecTag, sps),
			CodecTagString:  codecTag,
			ProfileIDC:      int(sps.ProfileTierLevel.GeneralProfileIDC),
			Level:           int(sps.ProfileTierLevel.GeneralLevelIDC),
			VideoLayout:     Mp4VideoLayoutMono,
		}

		// Look for Dolby Vision configuration box (dvvC, dvcC, or dvwC) in children.
		for _, child := range se.Children {
			boxType := child.Type()
			if IsDOVIBoxType(boxType) {
				if ub, ok := child.(*mp4.UnknownBox); ok {
					dovi, doviErr := ParseDOVIBox(ub.Payload())
					if doviErr != nil {
						return nil, e(doviErr, "reason", "failed to parse Dolby Vision box", "box", boxType)
					}
					dovi.FourCC = avdesc.DOVIFourCC(codecTag)
					info.DOVI = dovi
					info.DOVI.BoxType = boxType
				}
				break
			}
		}

		// If VPS declares multiple layers - it's MV-HEVC
		// Append the enhancement-layer codec descriptor so MimeCodecString
		// matches the Apple format: `hvc1.<base>,hvc1.<enh>`
		if vps := parseHvcCVPS(se.HvcC); vps != nil && vps.IsMultiLayer() {
			if enhPTL := mvhevcEnhancementPTL(vps); enhPTL != nil {
				info.VideoLayout = Mp4VideoLayoutMVHEVC
				info.EnhancementProfileIDC = int(enhPTL.GeneralProfileIDC)
				info.MimeCodecString = info.MimeCodecString + "," + hevcCodecStringFromPTL(codecTag, *enhPTL)
				return info, nil
			}
		}

		// Check for frame-packed stereo (SBS): st3d or HEVC SEI 45 in hvcC
		// PENDING(SS) if SEI 45 only in mdat and not hvcC, we don't see it from the moov - to test CPU/GPU outputs
		if layout := detectStereoFromVse(se); layout != Mp4VideoLayoutMono {
			info.VideoLayout = layout
		} else if layout := detectStereoFromSEI(se.HvcC); layout != Mp4VideoLayoutMono {
			info.VideoLayout = layout
		}
		return info, nil
	case "avc1", "avc3":
		if se.AvcC == nil {
			return nil, e("reason", "avcC box missing")
		}
		if len(se.AvcC.SPSnalus) == 0 {
			return nil, e("reason", "no SPS in avcC")
		}
		sps, err := avc.ParseSPSNALUnit(se.AvcC.SPSnalus[0], false)
		if err != nil {
			return nil, e(err, "reason", "failed to parse AVC SPS")
		}
		info := &CodecInfo{
			MimeCodecString: avc.CodecString(codecTag, sps),
			CodecTagString:  codecTag,
			ProfileIDC:      int(sps.Profile),
			Level:           int(sps.Level),
		}
		// Parse the SPS/PPS parameter sets for the SPS/PPS detail fields (and
		// retain them for slice-header analysis of the media samples).
		if params, perr := parseAVCParams(se.AvcC.SPSnalus, se.AvcC.PPSnalus); perr == nil {
			info.AVC = params
		}
		return info, nil
	default:
		return &CodecInfo{CodecTagString: codecTag}, nil
	}
}

// parseAVCParams parses SPS and PPS NAL units into AVCParams and retains the
// parsed parameter sets (keyed by id) for slice-header analysis. The NALUs may
// come from an avcC box (init segment) or from in-band sample data.
func parseAVCParams(spsNalus, ppsNalus [][]byte) (*AVCParams, error) {
	e := errors.T("parseAVCParams", errors.K.Invalid)
	if len(spsNalus) == 0 {
		return nil, e("reason", "no SPS NAL units")
	}

	spsMap := make(map[uint32]*avc.SPS)
	var firstSPS *avc.SPS
	for _, n := range spsNalus {
		s, err := avc.ParseSPSNALUnit(n, false)
		if err != nil {
			continue
		}
		spsMap[s.ParameterID] = s
		if firstSPS == nil {
			firstSPS = s
		}
	}
	if firstSPS == nil {
		return nil, e("reason", "failed to parse any AVC SPS")
	}

	ppsMap := make(map[uint32]*avc.PPS)
	var firstPPS *avc.PPS
	for _, n := range ppsNalus {
		p, err := avc.ParsePPSNALUnit(n, spsMap)
		if err != nil {
			continue
		}
		ppsMap[p.PicParameterSetID] = p
		if firstPPS == nil {
			firstPPS = p
		}
	}

	params := &AVCParams{
		MaxNumRefFrames:  firstSPS.NumRefFrames,
		PicOrderCntType:  firstSPS.PicOrderCntType,
		FrameMbsOnlyFlag: firstSPS.FrameMbsOnlyFlag,
		Width:            firstSPS.Width,
		Height:           firstSPS.Height,
		spsMap:           spsMap,
		ppsMap:           ppsMap,
	}
	if firstPPS != nil {
		params.EntropyCodingCABAC = firstPPS.EntropyCodingModeFlag
		params.Transform8x8Mode = firstPPS.Transform8x8ModeFlag
		params.WeightedPred = firstPPS.WeightedPredFlag
		params.WeightedBipredIDC = firstPPS.WeightedBipredIDC
		params.ConstrainedIntraPred = firstPPS.ConstrainedIntraPredFlag
		params.NumRefIdxL0Default = firstPPS.NumRefIdxI0DefaultActiveMinus1 + 1
		params.NumRefIdxL1Default = firstPPS.NumRefIdxI1DefaultActiveMinus1 + 1
	}
	return params, nil
}

// analyzeAVCSlices walks the media fragments' samples, parses every coded
// slice header, and summarizes the actual reference usage. maxRefFrames is the
// SPS max_num_ref_frames used to flag overflow.
func analyzeAVCSlices(
	segments []*mp4.MediaSegment,
	spsMap map[uint32]*avc.SPS,
	ppsMap map[uint32]*avc.PPS,
) *AVCSliceStats {

	stats := &AVCSliceStats{
		SliceTypeCounts:   make(map[string]int),
		NumRefIdxL0Counts: make(map[int]int),
		NumRefIdxL1Counts: make(map[int]int),
	}

	// Representative SPS for POC parameters (uniform in practice).
	var sps0 *avc.SPS
	for _, s := range spsMap {
		sps0 = s
		break
	}
	var pps0 *avc.PPS
	for _, p := range ppsMap {
		pps0 = p
		break
	}
	maxPocLsb := 0
	if sps0 != nil {
		maxPocLsb = 1 << (sps0.Log2MaxPicOrderCntLsbMinus4 + 4)
	}
	prevPocMsb, prevPocLsb := 0, 0

	// Per-frame picture order count and IDR flags, in decode order, to derive
	// the reorder depth (the actual B-frame reference span). Reordering only
	// happens with pic_order_cnt_type 0; types 1/2 are display==decode order.
	canReorder := sps0 != nil && sps0.PicOrderCntType == 0
	var pocs []int
	var idrFlags []bool
	curBRun := 0

	for _, seg := range segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(nil)
			if err != nil {
				continue
			}
			for i := range samples {
				nalus, err := avc.GetNalusFromSample(samples[i].Data)
				if err != nil {
					continue
				}
				for _, nalu := range nalus {
					if len(nalu) == 0 {
						continue
					}
					nt := avc.GetNaluType(nalu[0])
					if nt == avc.NALU_SEI && stats.X264 == nil {
						if x264 := parseX264SettingsFromSEI(nalu, sps0); x264 != nil {
							stats.X264 = x264
						}
						continue
					}
					if nt != avc.NALU_NON_IDR && nt != avc.NALU_IDR {
						continue
					}
					stats.Count++
					// Slice type is readable without SPS/PPS (it precedes the
					// parameter-set id in the slice header).
					if st, e := avc.GetSliceTypeFromNALU(nalu); e == nil {
						stats.SliceTypeCounts[sliceTypeName(st)]++
					}
					// Full slice-header parse needs the matching SPS/PPS. A
					// failure means the slice doesn't match the active parameter
					// sets (foreign/absent init) — count it as a mismatch signal.
					sh, err := avc.ParseSliceHeader(nalu, spsMap, ppsMap)
					if err != nil {
						stats.UnparsedSlices++
						continue
					}
					base := avc.SliceType(sh.SliceType % 5)

					// Active L0 references; only P/SP/B slices reference frames.
					l0 := 0
					switch base {
					case avc.SLICE_P, avc.SLICE_SP, avc.SLICE_B:
						l0 = int(sh.NumRefIdxL0ActiveMinus1) + 1
					}
					stats.NumRefIdxL0Counts[l0]++
					if l0 > stats.MaxNumRefIdxL0Active {
						stats.MaxNumRefIdxL0Active = l0
					}

					// B-slice specifics: L1 (forward) references + B-run length.
					if base == avc.SLICE_B {
						stats.HasBFrames = true
						l1 := int(sh.NumRefIdxL1ActiveMinus1) + 1
						stats.NumRefIdxL1Counts[l1]++
						if l1 > stats.MaxNumRefIdxL1Active {
							stats.MaxNumRefIdxL1Active = l1
						}
						curBRun++
						if curBRun > stats.MaxConsecutiveBFrames {
							stats.MaxConsecutiveBFrames = curBRun
						}
					} else {
						curBRun = 0
					}

					// Picture order count (display order) for reorder-depth.
					nalRefIdc := (nalu[0] >> 5) & 0x03
					poc := len(pocs) // default: decode order (POC type 1/2, no reorder)
					if sps0 != nil {
						if nt == avc.NALU_IDR {
							poc, prevPocMsb, prevPocLsb = 0, 0, 0
						} else if sps0.PicOrderCntType == 0 && maxPocLsb > 0 {
							lsb := int(sh.PicOrderCntLsb)
							msb := prevPocMsb
							if lsb < prevPocLsb && prevPocLsb-lsb >= maxPocLsb/2 {
								msb = prevPocMsb + maxPocLsb
							} else if lsb > prevPocLsb && lsb-prevPocLsb > maxPocLsb/2 {
								msb = prevPocMsb - maxPocLsb
							}
							poc = msb + lsb
							if nalRefIdc != 0 { // only reference pictures update prev
								prevPocMsb, prevPocLsb = msb, lsb
							}
						}
					}
					pocs = append(pocs, poc)
					idrFlags = append(idrFlags, nt == avc.NALU_IDR)
				}
			}
		}
	}

	if canReorder {
		stats.MaxReorderDepthFrames = maxReorderDepth(pocs, idrFlags)
	}
	addAVCCompatibilityWarnings(stats, sps0, pps0)
	return stats
}

func parseX264SettingsFromSEI(nalu []byte, sps *avc.SPS) *AVCX264Settings {
	msgs, err := avc.ParseSEINalu(nalu, sps)
	if err != nil && len(msgs) == 0 {
		return nil
	}
	for _, msg := range msgs {
		unregistered, ok := msg.(*sei.UnregisteredSEI)
		if !ok {
			continue
		}
		payload := unregistered.Payload()
		if len(payload) <= 16 {
			continue
		}
		settings := strings.TrimRight(string(payload[16:]), "\x00")
		if x264 := parseX264SettingsText(settings); x264 != nil {
			return x264
		}
	}
	return nil
}

func parseX264SettingsText(settings string) *AVCX264Settings {
	if !strings.Contains(settings, "x264") || !strings.Contains(settings, "options:") {
		return nil
	}
	settings = sanitizeString(settings)
	x264 := &AVCX264Settings{Raw: settings}

	opts := parseEncoderOptions(settings)
	if v, ok := parseOptionInt(opts, "ref"); ok {
		x264.Ref = v
	}
	if v, ok := parseOptionBool(opts, "cabac"); ok {
		x264.CABAC = &v
	}
	if v, ok := parseOptionBool(opts, "8x8dct"); ok {
		x264.Transform8x8 = &v
	}
	if v, ok := parseOptionInt(opts, "weightp"); ok {
		x264.WeightP = &v
	}
	if v, ok := parseOptionInt(opts, "bframes"); ok {
		x264.BFrames = &v
	}
	if v, ok := parseOptionInt(opts, "keyint"); ok {
		x264.KeyInt = v
	}
	if v, ok := parseOptionBool(opts, "stitchable"); ok {
		x264.Stitchable = &v
	}
	if v, ok := parseOptionBool(opts, "constrained_intra"); ok {
		x264.ConstrainedIntra = &v
	}
	return x264
}

func parseEncoderOptions(settings string) map[string]string {
	idx := strings.Index(settings, "options:")
	if idx < 0 {
		return nil
	}
	opts := make(map[string]string)
	for _, field := range strings.Fields(settings[idx+len("options:"):]) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || key == "" {
			continue
		}
		opts[key] = strings.TrimRight(value, "\x00")
	}
	return opts
}

func parseOptionInt(opts map[string]string, key string) (int, bool) {
	value, ok := opts[key]
	if !ok {
		return 0, false
	}
	if i := strings.IndexByte(value, ':'); i >= 0 {
		value = value[:i]
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func parseOptionBool(opts map[string]string, key string) (bool, bool) {
	value, ok := parseOptionInt(opts, key)
	if !ok {
		return false, false
	}
	return value != 0, true
}

func addAVCCompatibilityWarnings(stats *AVCSliceStats, sps *avc.SPS, pps *avc.PPS) {
	if stats == nil || stats.X264 == nil {
		return
	}
	x264 := stats.X264
	if sps != nil && x264.Ref > 0 && uint(x264.Ref) != sps.NumRefFrames {
		stats.CompatibilityWarnings = append(stats.CompatibilityWarnings,
			fmt.Sprintf("x264 ref=%d but active SPS max_num_ref_frames=%d", x264.Ref, sps.NumRefFrames))
	}
	if pps == nil {
		return
	}
	ppsL0 := int(pps.NumRefIdxI0DefaultActiveMinus1) + 1
	if x264.Ref > 0 && x264.Ref != ppsL0 {
		stats.CompatibilityWarnings = append(stats.CompatibilityWarnings,
			fmt.Sprintf("x264 ref=%d but active PPS num_ref_idx_l0_default_active=%d", x264.Ref, ppsL0))
	}
	if x264.CABAC != nil && *x264.CABAC != pps.EntropyCodingModeFlag {
		stats.CompatibilityWarnings = append(stats.CompatibilityWarnings,
			fmt.Sprintf("x264 cabac=%t but active PPS entropy_coding_cabac=%t", *x264.CABAC, pps.EntropyCodingModeFlag))
	}
	if x264.Transform8x8 != nil && *x264.Transform8x8 != pps.Transform8x8ModeFlag {
		stats.CompatibilityWarnings = append(stats.CompatibilityWarnings,
			fmt.Sprintf("x264 8x8dct=%t but active PPS transform_8x8_mode=%t", *x264.Transform8x8, pps.Transform8x8ModeFlag))
	}
	if x264.WeightP != nil && (*x264.WeightP > 0) != pps.WeightedPredFlag {
		stats.CompatibilityWarnings = append(stats.CompatibilityWarnings,
			fmt.Sprintf("x264 weightp=%d but active PPS weighted_pred=%t", *x264.WeightP, pps.WeightedPredFlag))
	}
}

// maxReorderDepth returns the maximum (decode_index - display_index) over all
// frames, computed per GOP (reset at each IDR, which flushes the DPB). It is
// the number of frames the decoder must hold for B-frame reordering — i.e. the
// actual reference span B-frames introduce between decode and display order.
func maxReorderDepth(pocs []int, idr []bool) int {
	maxDepth, i, n := 0, 0, len(pocs)
	for i < n {
		// GOP is [i, j): up to (but not including) the next IDR.
		j := i + 1
		for j < n && !idr[j] {
			j++
		}
		gop := pocs[i:j]
		order := make([]int, len(gop))
		for k := range order {
			order[k] = k
		}
		sort.SliceStable(order, func(a, b int) bool { return gop[order[a]] < gop[order[b]] })
		rank := make([]int, len(gop))
		for r, k := range order {
			rank[k] = r
		}
		for k := range gop {
			if d := k - rank[k]; d > maxDepth {
				maxDepth = d
			}
		}
		i = j
	}
	return maxDepth
}

// sliceTypeName maps an AVC slice_type (0-9) to its base I/P/B/SP/SI name.
func sliceTypeName(t avc.SliceType) string {
	switch avc.SliceType(t % 5) {
	case avc.SLICE_P:
		return "P"
	case avc.SLICE_B:
		return "B"
	case avc.SLICE_I:
		return "I"
	case avc.SLICE_SP:
		return "SP"
	case avc.SLICE_SI:
		return "SI"
	}
	return "?"
}

// parseHvcCVPS finds and decodes the VPS NALU embedded in an hvcC sample entry.
// Returns nil if no VPS is present or if it fails to parse.
func parseHvcCVPS(hvcC *mp4.HvcCBox) *hevc.VPS {
	for _, arr := range hvcC.NaluArrays {
		if arr.NaluType() != hevc.NALU_VPS || len(arr.Nalus) == 0 {
			continue
		}
		vps, err := hevc.ParseVPSNALUnit(arr.Nalus[0])
		if err != nil {
			return nil
		}
		return vps
	}
	return nil
}

// mvhevcEnhancementPTL returns the profile_tier_level of the MV-HEVC enhancement layer
// (for MV-HEVC this is the Multiview Main profile idc=6)
func mvhevcEnhancementPTL(vps *hevc.VPS) *hevc.ProfileTierLevel {
	basePTL := vps.ProfileTierLevel
	for i := range vps.ExtProfileTierLevels {
		ptl := vps.ExtProfileTierLevels[i]
		if ptl.GeneralProfileIDC == 0 {
			continue // placeholder/copy of base
		}
		if ptl.GeneralProfileIDC == basePTL.GeneralProfileIDC &&
			ptl.GeneralProfileSpace == basePTL.GeneralProfileSpace {
			continue
		}
		return &ptl
	}
	return nil
}

// detectStereoFromVse looks for the `st3d` box
// Returns Mp4VideoLayoutMono when absent or stereo_mode is monoscopic (0).
//
//	stereo_mode == 1 → top-bottom
//	stereo_mode == 2 → left-right (side-by-side)
//	other values treated as mono
func detectStereoFromVse(vse *mp4.VisualSampleEntryBox) Mp4VideoLayout {
	for _, child := range vse.Children {
		if child.Type() != "st3d" {
			continue
		}
		raw, ok := child.(*mp4.UnknownBox)
		if !ok {
			return Mp4VideoLayoutMono
		}
		payload := raw.Payload()
		// UnknownBox.Payload includes the FullBox version+flags (4 bytes); the
		// next byte is stereo_mode.
		if len(payload) < 5 {
			return Mp4VideoLayoutMono
		}
		switch payload[4] {
		case 1:
			return Mp4VideoLayoutTb
		case 2:
			return Mp4VideoLayoutSbs
		}
		return Mp4VideoLayoutMono
	}
	return Mp4VideoLayoutMono
}

// detectStereoFromSEI scans hvcC for SEI 45 (frame packing)
// Returns Mp4VideoLayoutMono if no usable SEI 45 is present.
//
// HEVC SEI 45 payload (D.2.16) starts with:
//
//	frame_packing_arrangement_id              ue(v)
//	frame_packing_arrangement_cancel_flag     u(1)
//	if !cancel_flag:
//	  frame_packing_arrangement_type          u(7)   ← what we want
//
// frame_packing_arrangement_type values per HEVC D.2.16 Table D.6:
//
//	3 = side-by-side, 4 = top-bottom
func detectStereoFromSEI(hvcC *mp4.HvcCBox) Mp4VideoLayout {
	for _, arr := range hvcC.NaluArrays {
		if arr.NaluType() != hevc.NALU_SEI_PREFIX {
			continue
		}
		for _, nalu := range arr.Nalus {
			msgs, err := hevc.ParseSEINalu(nalu, nil)
			if err != nil {
				continue
			}
			for _, m := range msgs {
				if m.Type() != sei.SEIFramePackingArrangementType {
					continue
				}
				if t, ok := framePackingArrangementType(m.Payload()); ok {
					switch t {
					case 3:
						return Mp4VideoLayoutSbs
					case 4:
						return Mp4VideoLayoutTb
					}
				}
			}
		}
	}
	return Mp4VideoLayoutMono
}

// framePackingArrangementType extracts frame_packing_arrangement_type for SEI 45
// Returns the type and true if cancel_flag is false; false otherwise (no type present, error).
func framePackingArrangementType(payload []byte) (byte, bool) {
	if len(payload) == 0 {
		return 0, false
	}
	r := mp4bits.NewEBSPReader(bytes.NewReader(payload))
	r.ReadExpGolomb() // frame_packing_arrangement_id
	cancel := r.Read(1)
	if r.AccError() != nil || cancel == 1 {
		return 0, false
	}
	t := r.Read(7) // frame_packing_arrangement_type
	if r.AccError() != nil {
		return 0, false
	}
	return byte(t), true
}

// hevcCodecStringFromPTL formats an RFC 6381 HEVC codec descriptor from a
// ProfileTierLevel alone. Mirrors hevc.CodecString but accepts a PTL directly
// — used for the MV-HEVC enhancement layer where the PTL comes from the VPS
// extension array, not from an SPS.
// TODO: duplicates hevc.CodecString from the imported mp4ff library; replace with hevc.CodecString once it accepts a bare ProfileTierLevel.
func hevcCodecStringFromPTL(sampleEntry string, ptl hevc.ProfileTierLevel) string {
	var profilePart string
	switch ptl.GeneralProfileSpace {
	case 1:
		profilePart = "A"
	case 2:
		profilePart = "B"
	case 3:
		profilePart = "C"
	}
	profilePart += fmt.Sprintf("%d", ptl.GeneralProfileIDC)

	flagsPart := fmt.Sprintf("%X", bits.Reverse32(ptl.GeneralProfileCompatibilityFlags))

	var levelPart string
	if ptl.GeneralTierFlag {
		levelPart = "H"
	} else {
		levelPart = "L"
	}
	levelPart += fmt.Sprintf("%d", ptl.GeneralLevelIDC)

	cif := ptl.GeneralConstraintIndicatorFlags
	nrBytes := 6
	for i := 0; i < 5; i++ { // Remove trailing zero bytes
		if cif&0xff == 0 {
			cif = cif >> 8
			nrBytes--
		} else {
			break
		}
	}
	constraintBytes := ""
	for i := 0; i < nrBytes; i++ {
		constraintBytes += fmt.Sprintf(".%X", (cif>>((nrBytes-1-i)*8))&0xff)
	}

	return fmt.Sprintf("%s.%s.%s.%s%s", sampleEntry, profilePart, flagsPart, levelPart, constraintBytes)
}

// LevelName returns a human-readable level string for the given codec 4CC and
// level IDC value, e.g. LevelName("avc1", 40) == "4.0".
// Returns an empty string if the codec has no level IDC or the value is zero.
//
// AVC level_idc is defined in ISO/IEC 14496-10 Table A-1: level = level_idc / 10.
// HEVC general_level_idc is defined in ISO/IEC 23008-2 Table A-1: level = general_level_idc / 30.
func LevelName(codecTag string, levelIDC int) string {
	if levelIDC == 0 {
		return ""
	}
	switch codecTag {
	case "avc1", "avc3":
		return fmt.Sprintf("%.1f", float64(levelIDC)/10)
	case "hvc1", "hev1":
		return fmt.Sprintf("%.1f", float64(levelIDC)/30)
	}
	return ""
}

// ProfileName returns a human-readable profile name for the given codec 4CC
// and profile IDC value, e.g. ProfileName("avc1", 100) == "High".
// Returns an empty string if the codec or profile IDC is not recognized.
//
// AVC profile IDCs are defined in ISO/IEC 14496-10.
// HEVC profile IDCs are defined in ISO/IEC 23008-2.
// AAC profile IDCs are Audio Object Type values from ISO/IEC 14496-3.
func ProfileName(codecTag string, profileIDC int) string {
	switch codecTag {
	case "avc1", "avc3":
		return avcProfileNames[profileIDC]
	case "hvc1", "hev1":
		return hevcProfileNames[profileIDC]
	case "mp4a":
		return aacProfileNames[profileIDC]
	}
	return ""
}

// avcProfileNames maps AVC (H.264) profile_idc to profile name
// (ISO/IEC 14496-10 Annex A).
var avcProfileNames = map[int]string{
	66:  "Baseline",
	77:  "Main",
	88:  "Extended",
	100: "High",
	110: "High 10",
	122: "High 4:2:2",
	244: "High 4:4:4 Predictive",
	83:  "Scalable Baseline",
	86:  "Scalable High",
	128: "Stereo High",
	138: "Multiview High",
	139: "Multiview Depth High",
}

// hevcProfileNames maps HEVC (H.265) general_profile_idc to profile name
// (ISO/IEC 23008-2 Annex A).
var hevcProfileNames = map[int]string{
	1:  "Main",
	2:  "Main 10",
	3:  "Main Still Picture",
	4:  "Range Extensions",
	5:  "High Throughput",
	6:  "Multiview Main",
	7:  "Scalable Main",
	8:  "3D Main",
	9:  "Screen Content Coding Extensions",
	10: "Scalable Range Extensions",
	11: "High Throughput Screen Content Coding Extensions",
}

// aacProfileNames maps AAC Audio Object Type (profile_idc) to profile name
// (ISO/IEC 14496-3).
var aacProfileNames = map[int]string{
	1:  "Main",
	2:  "LC",
	3:  "SSR",
	4:  "LTP",
	5:  "SBR",
	6:  "Scalable",
	23: "LD",
	29: "PS",
	39: "ER AAC ELD",
	42: "USAC",
}
