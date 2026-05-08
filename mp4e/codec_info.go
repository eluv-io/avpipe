package mp4e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/Eyevinn/mp4ff/aac"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/eluv-io/errors-go"
)

// CodecInfo contains information about a media stream's codecs
type CodecInfo struct {
	// CodecID is the sample description entry 4-character code (in the MP4
	// "stsd" box) as registered by the MP4RA; e.g. "hvc1", "avc1", "ec-3"
	CodecID string `json:"codec_id"`

	// CodecParameter specifies the decoder requirements for a media stream, as
	// specified by RFC 6381; e.g. "hvc1.2.4.L120.90" or "avc1.640028"
	CodecParameter string `json:"codec_parameter,omitempty"`

	// ProfileIDC is the codec profile IDC. The value is defined separately by
	// each codec; e.g. 2 = HEVC Main 10, 100 = AVC High
	ProfileIDC int `json:"profile_idc,omitempty"`

	// LevelIDC is the codec level IDC. The value id defined separately by each
	// codec:
	//  * Divide by 30 for HEVC, e.g. 120 → 4.0
	//  * Divide by 10 for AVC, e.g. 40  → 4.0
	LevelIDC int `json:"level_idc,omitempty"`

	// AudioChannels is the number of audio channels
	AudioChannels int `json:"audio_channels,omitempty"`

	// EC3 is set only when the codec is ec-3
	EC3 *EC3Info `json:"ec3,omitempty"`
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
		ProfileName: ProfileName(c.CodecID, c.ProfileIDC),
		LevelName:   LevelName(c.CodecID, c.LevelIDC),
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
			info = &CodecInfo{CodecID: se.Type()}
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
		return &CodecInfo{CodecID: se.Type()}, nil
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
		CodecParameter: "ec-3",
		CodecID:        "ec-3",
		AudioChannels:  nChannels,
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
		CodecParameter: fmt.Sprintf("mp4a.40.%d", asc.ObjectType),
		CodecID:        "mp4a",
		ProfileIDC:     int(asc.ObjectType),
		AudioChannels:  int(se.ChannelCount),
	}, nil
}

func parseVisualSampleEntryBox(se *mp4.VisualSampleEntryBox) (*CodecInfo, error) {
	codecID := se.Type()
	e := errors.T("parseVisualSampleEntryBox", errors.K.Invalid, "codecID", codecID)

	switch codecID {
	case "hvc1", "hev1":
		if se.HvcC == nil {
			return nil, e("reason", "hvcC box missing")
		}
		for _, arr := range se.HvcC.NaluArrays {
			if arr.NaluType() != hevc.NALU_SPS || len(arr.Nalus) == 0 {
				continue
			}
			sps, err := hevc.ParseSPSNALUnit(arr.Nalus[0])
			if err != nil {
				return nil, e(err, "reason", "failed to parse HEVC SPS")
			}
			return &CodecInfo{
				CodecParameter: hevc.CodecString(codecID, sps),
				CodecID:        codecID,
				ProfileIDC:     int(sps.ProfileTierLevel.GeneralProfileIDC),
				LevelIDC:       int(sps.ProfileTierLevel.GeneralLevelIDC),
			}, nil
		}
		return nil, e("reason", "no SPS found in hvcC")
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
			CodecParameter: avc.CodecString(codecID, sps),
			CodecID:        codecID,
			ProfileIDC:     int(sps.Profile),
			LevelIDC:       int(sps.Level),
		}, nil
	default:
		return &CodecInfo{CodecID: codecID}, nil
	}
}

// LevelName returns a human-readable level string for the given codec 4CC and
// level IDC value, e.g. LevelName("avc1", 40) == "4.0".
// Returns an empty string if the codec has no level IDC or the value is zero.
//
// AVC level_idc is defined in ISO/IEC 14496-10 Table A-1: level = level_idc / 10.
// HEVC general_level_idc is defined in ISO/IEC 23008-2 Table A-1: level = general_level_idc / 30.
func LevelName(codecID string, levelIDC int) string {
	if levelIDC == 0 {
		return ""
	}
	switch codecID {
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
func ProfileName(codecID string, profileIDC int) string {
	switch codecID {
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
