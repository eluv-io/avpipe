package mp4e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/bits"
	"strings"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	mp4bits "github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/Eyevinn/mp4ff/sei"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/errors-go"
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
	EC3 *EC3Info `json:"ec3,omitempty"`

	// VideoLayout describes the stereoscopic layout (mono, sbs, mvhevc).
	VideoLayout goavpipe.VideoLayout `json:"video_layout,omitempty"`

	// EnhancementProfileIDC is the enhancement-layer general_profile_idc
	// Only meaningful for VideoLayout == VideoLayoutMVHEVC (typically '6')
	EnhancementProfileIDC int `json:"enhancement_profile_idc,omitempty"`
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

// EC3Info holds E-AC-3-specific fields (Dolby Digital Plus / Atmos)
type EC3Info struct {
	// ChanMap is the custom channel map bitmask from the MP4 dec3 box
	// (ETSI TS 102 366 Table E.1.4)
	ChanMap uint16 `json:"chan_map"`

	// JOC indicates Joint Object Coding (Dolby Atmos). True when the
	// flag_ec3_extension_type_a bit is set in the MP4 dec3 box extension
	// (ETSI TS 103 420)
	JOC bool `json:"joc"`

	// ComplexityIndex is the Atmos object complexity index
	// (complexity_index_type_a from ETSI TS 103 420). Only meaningful when
	// JOC is true.
	ComplexityIndex int `json:"complexity_index,omitempty"`
}

// ChanMapHex returns ChanMap as an uppercase hex string; e.g. "F801"
func (e EC3Info) ChanMapHex() string {
	return fmt.Sprintf("%04X", e.ChanMap)
}

// ec3ChanMapNames lists the channel names for each bit of the EC-3 custom
// channel map, ordered from MSB (bit 15) to LSB (bit 0), per ETSI TS 102 366
// Table E.1.4.
var ec3ChanMapNames = [16]string{
	"L", "C", "R", "Ls", "Rs", "Lc/Rc", "Lrs/Rrs", "Cs",
	"Ts", "Lsd/Rsd", "Lw/Rw", "Vhl/Vhr", "Vhc", "Lts/Rts", "LFE2", "LFE",
}

// ChanMapString returns the channel names encoded in ChanMap as a
// space-separated string; e.g. "L C R Ls Rs LFE"
func (e EC3Info) ChanMapString() string {
	var names []string
	for i, name := range ec3ChanMapNames {
		if e.ChanMap&(1<<(15-i)) != 0 {
			names = append(names, name)
		}
	}
	return strings.Join(names, " ")
}

// MarshalJSON adds an additional chan_map_hex field alongside the numeric
// chan_map
func (e EC3Info) MarshalJSON() ([]byte, error) {
	type alias EC3Info
	return json.Marshal(&struct {
		alias
		ChanMapHex string `json:"chan_map_hex,omitempty"`
	}{
		alias:      alias(e),
		ChanMapHex: e.ChanMapHex(),
	})
}

// ExtractCodecInfo decodes an MP4 container and returns codec information for
// all tracks. Supports AVC (avc1/avc3) and HEVC (hvc1/hev1) video tracks, and
// E-AC-3 (ec-3) and AAC (mp4a) audio tracks.
func ExtractCodecInfo(r io.Reader) (infos []*CodecInfo, err error) {
	e := errors.T("ExtractCodecInfo", errors.K.Invalid.Default())

	mp4Data, err := mp4.DecodeFile(r)
	if err != nil {
		return nil, e("reason", "failed to parse MP4", err)
	}

	// Fragmented MP4 (fMP4 init segment): moov is under mp4Data.Init.
	// Regular MP4: moov is directly under mp4Data.
	var moov *mp4.MoovBox
	if mp4Data.Init != nil {
		moov = mp4Data.Init.Moov
	} else if mp4Data.Moov != nil {
		moov = mp4Data.Moov
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
			info = &CodecInfo{CodecTagString: se.Type()}
		}

		if err != nil {
			return nil, e("reason", "failed to parse MP4 sample entry box", err)
		} else if info != nil {
			infos = append(infos, info)
		}
	}

	return
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
		EC3: &EC3Info{
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

func parseVisualSampleEntryBox(se *mp4.VisualSampleEntryBox) (*CodecInfo, error) {
	codecTag := se.Type()
	e := errors.T("parseVisualSampleEntryBox", errors.K.Invalid, "codecTag", codecTag)

	switch codecTag {
	case "hvc1", "hev1":
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
			VideoLayout:     goavpipe.VideoLayoutMono,
		}

		// If VPS declares multiple layers - it's MV-HEVC
		// Append the enhancement-layer codec descriptor so MimeCodecString
		// matches the Apple format: `hvc1.<base>,hvc1.<enh>`
		if vps := parseHvcCVPS(se.HvcC); vps != nil && vps.IsMultiLayer() {
			if enhPTL := mvhevcEnhancementPTL(vps); enhPTL != nil {
				info.VideoLayout = goavpipe.VideoLayoutMVHEVC
				info.EnhancementProfileIDC = int(enhPTL.GeneralProfileIDC)
				info.MimeCodecString = info.MimeCodecString + "," + hevcCodecStringFromPTL(codecTag, *enhPTL)
				return info, nil
			}
		}

		// Check for frame-packed stereo (SBS): st3d or HEVC SEI 45 in hvcC
		// PENDING(SS) if SEI 45 only in mdat and not hvcC we don't see it from the moov - to test CPU/GPU outputs
		if layout := detectStereoFromVse(se); layout != goavpipe.VideoLayoutMono {
			info.VideoLayout = layout
		} else if layout := detectStereoFromSEI(se.HvcC); layout != goavpipe.VideoLayoutMono {
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
		return &CodecInfo{
			MimeCodecString: avc.CodecString(codecTag, sps),
			CodecTagString:  codecTag,
			ProfileIDC:      int(sps.Profile),
			Level:           int(sps.Level),
		}, nil
	default:
		return &CodecInfo{CodecTagString: codecTag}, nil
	}
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
// Returns VideoLayoutMono when absent or stereo_mode is monoscopic (0).
//
//	stereo_mode == 1 → top-bottom
//	stereo_mode == 2 → left-right (side-by-side)
//	other values treated as mono
func detectStereoFromVse(vse *mp4.VisualSampleEntryBox) goavpipe.VideoLayout {
	for _, child := range vse.Children {
		if child.Type() != "st3d" {
			continue
		}
		raw, ok := child.(*mp4.UnknownBox)
		if !ok {
			return goavpipe.VideoLayoutMono
		}
		payload := raw.Payload()
		// UnknownBox.Payload includes the FullBox version+flags (4 bytes); the
		// next byte is stereo_mode.
		if len(payload) < 5 {
			return goavpipe.VideoLayoutMono
		}
		switch payload[4] {
		case 1:
			return goavpipe.VideoLayoutTb
		case 2:
			return goavpipe.VideoLayoutSbs
		}
		return goavpipe.VideoLayoutMono
	}
	return goavpipe.VideoLayoutMono
}

// detectStereoFromSEI scans hvcC for SEI 45 (frame packing)
// Returns VideoLayoutMono if no usable SEI 45 is present.
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
func detectStereoFromSEI(hvcC *mp4.HvcCBox) goavpipe.VideoLayout {
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
						return goavpipe.VideoLayoutSbs
					case 4:
						return goavpipe.VideoLayoutTb
					}
				}
			}
		}
	}
	return goavpipe.VideoLayoutMono
}

// framePackingArrangementType extracts frame_packing_arrangement_type fro SEI 45
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
