package mp4e

import (
	"errors"
	"fmt"
	"strings"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/Eyevinn/mp4ff/sei"
)

const (
	hdr10Primaries = 9
	hdr10Transfer  = 16
	hdr10Matrix    = 9
	hdr10Profile   = 2

	hdrSEISampleScanLimitSeconds uint64 = 120
)

type HDRInfo struct {
	TrackID uint32
	CodecID string

	Video HDRVideoInfo
	HvcC  HDRHvcCInfo
	Colr  HDRColrInfo
	Clli  HDRClliInfo
	Mdcv  HDRMdcvInfo
	SEI   HDRSEIInfo

	Checks []HDRCheck
	Errors []string
}

type HDRVideoInfo struct {
	Width                    uint32
	Height                   uint32
	SampleEntryWidth         uint32
	SampleEntryHeight        uint32
	TrackWidth               uint32
	TrackHeight              uint32
	PixelAspectH             uint32
	PixelAspectV             uint32
	SPSDisplayWidth          uint32
	SPSDisplayHeight         uint32
	CodedWidth               uint32
	CodedHeight              uint32
	ConformanceWindowPresent bool
	ConformanceWindowLeft    uint32
	ConformanceWindowRight   uint32
	ConformanceWindowTop     uint32
	ConformanceWindowBottom  uint32
}

type HDRCheck struct {
	Name   string
	OK     bool
	Detail string
}

type HDRHvcCInfo struct {
	Present                 bool
	ProfileIDC              byte
	TierFlag                bool
	LevelIDC                byte
	ChromaFormatIDC         byte
	BitDepthLuma            byte
	BitDepthChroma          byte
	NALULengthSize          byte
	SPSPresent              bool
	SPSProfileIDC           byte
	SPSTierFlag             bool
	SPSLevelIDC             byte
	SPSChromaFormatIDC      byte
	SPSBitDepthLuma         byte
	SPSBitDepthChroma       byte
	VUIPresent              bool
	SampleAspectRatioWidth  uint
	SampleAspectRatioHeight uint
	VideoSignalTypePresent  bool
	ColourDescription       bool
	VideoFullRangeFlag      bool
	ColourPrimaries         byte
	TransferCharacteristics byte
	MatrixCoefficients      byte
}

type HDRFieldInfo struct {
	Codec                   string `json:"codec"`
	Width                   string `json:"width"`
	Height                  string `json:"height"`
	AspectRatio             string `json:"aspect_ratio"`
	SampleEntryWidth        string `json:"sample_entry_width"`
	SampleEntryHeight       string `json:"sample_entry_height"`
	TrackWidth              string `json:"track_width"`
	TrackHeight             string `json:"track_height"`
	SPSDisplayWidth         string `json:"sps_display_width"`
	SPSDisplayHeight        string `json:"sps_display_height"`
	CodedWidth              string `json:"coded_width"`
	CodedHeight             string `json:"coded_height"`
	ConformanceWindow       string `json:"conformance_window"`
	PixelAspectRatio        string `json:"pixel_aspect_ratio"`
	Level                   string `json:"level"`
	Profile                 string `json:"profile"`
	ChromaFormat            string `json:"chroma_format"`
	NALULengthSize          string `json:"nalu_length_size"`
	ColrType                string `json:"colr_type"`
	VUIPresent              string `json:"vui_present"`
	VideoSignalTypePresent  string `json:"video_signal_type_present"`
	ColourDescription       string `json:"colour_description_present"`
	BitDepth                string `json:"bit_depth"`
	ColorPrimaries          string `json:"color_primaries"`
	TransferCharacteristics string `json:"transfer_characteristics"`
	MatrixCoefficients      string `json:"matrix_coefficients"`
	ColorRange              string `json:"color_range"`
	MasteringDisplay        string `json:"mastering_display"`
	MaxCLLFALL              string `json:"max_cll_fall"`
	MaxLuma                 string `json:"max_luma"`
	MinLuma                 string `json:"min_luma"`
}

type HDRReport struct {
	ParseWarning string           `json:"parse_warning,omitempty"`
	HDR          HDRReportSection `json:"hdr"`
	Info         *HDRFieldInfo    `json:"info,omitempty"`
}

type HDRReportSection struct {
	Track  *HDRTrackInfo    `json:"track,omitempty"`
	Checks []HDRReportCheck `json:"checks"`
}

type HDRTrackInfo struct {
	ID    uint32 `json:"id,omitempty"`
	Codec string `json:"codec,omitempty"`
}

type HDRReportCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type HDRColrInfo struct {
	Present                 bool
	ColorType               string
	ColorPrimaries          uint16
	TransferCharacteristics uint16
	MatrixCoefficients      uint16
	FullRangeFlag           bool
}

type HDRClliInfo struct {
	Present                 bool
	MaxContentLightLevel    uint16
	MaxPicAverageLightLevel uint16
}

type HDRMdcvInfo struct {
	Present                      bool
	DisplayPrimariesX            [3]uint16
	DisplayPrimariesY            [3]uint16
	WhitePointX                  uint16
	WhitePointY                  uint16
	MaxDisplayMasteringLuminance uint32
	MinDisplayMasteringLuminance uint32
}

type HDRSEIInfo struct {
	Mastering               *HDRMdcvInfo
	ContentLightLevel       *HDRClliInfo
	EncodingSettings        string
	MinLuma                 string
	MaxLuma                 string
	MasteringSources        []string
	ContentLightSources     []string
	EncodingSettingsSources []string
	SamplesScanned          int
	NALUsScanned            int
	ParseErrors             []string
}

func ValidateHDR(file *mp4.File) (*HDRInfo, error) {
	info := &HDRInfo{}
	if file == nil {
		return info, fmt.Errorf("nil MP4 file")
	}

	moov, trak, vse, ok := findHEVCVideoSampleEntry(file)
	if !ok {
		info.addCheck("hvcC/SPS", false, "no HEVC video sample entry found")
		info.addCheck("colr", false, "no HEVC video sample entry found")
		info.addCheck("clli", false, "no HEVC video sample entry found")
		info.addCheck("mdcv", false, "no HEVC video sample entry found")
		info.addCheck("HEVC SEI", false, "no HEVC video sample entry found")
		return info, nil
	}

	if trak.Tkhd != nil {
		info.TrackID = trak.Tkhd.TrackID
	}
	info.CodecID = vse.Type()

	sps := info.validateHvcC(vse)
	info.setVideoInfo(trak, vse, sps)
	info.validateColr(vse)
	info.validateClli(vse)
	info.validateMdcv(vse)
	info.validateSEI(file, moov, trak, vse, sps)
	return info, nil
}

func (h *HDRInfo) String() string {
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "hdr:\n")
	if h.TrackID != 0 || h.CodecID != "" {
		_, _ = fmt.Fprintf(&sb, "  track: id=%d codec=%s\n", h.TrackID, h.CodecID)
	}
	for _, check := range h.Checks {
		status := "FAIL"
		if check.OK {
			status = "OK"
		}
		_, _ = fmt.Fprintf(&sb, "  [%s] %s: %s\n", status, check.Name, check.Detail)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (h *HDRInfo) InfoString() string {
	info := h.FieldInfo()
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "info:\n")
	_, _ = fmt.Fprintf(&sb, "  codec: %s\n", info.Codec)
	_, _ = fmt.Fprintf(&sb, "  width: %s\n", info.Width)
	_, _ = fmt.Fprintf(&sb, "  height: %s\n", info.Height)
	_, _ = fmt.Fprintf(&sb, "  aspect_ratio: %s\n", info.AspectRatio)
	_, _ = fmt.Fprintf(&sb, "  sample_entry_width: %s\n", info.SampleEntryWidth)
	_, _ = fmt.Fprintf(&sb, "  sample_entry_height: %s\n", info.SampleEntryHeight)
	_, _ = fmt.Fprintf(&sb, "  track_width: %s\n", info.TrackWidth)
	_, _ = fmt.Fprintf(&sb, "  track_height: %s\n", info.TrackHeight)
	_, _ = fmt.Fprintf(&sb, "  sps_display_width: %s\n", info.SPSDisplayWidth)
	_, _ = fmt.Fprintf(&sb, "  sps_display_height: %s\n", info.SPSDisplayHeight)
	_, _ = fmt.Fprintf(&sb, "  coded_width: %s\n", info.CodedWidth)
	_, _ = fmt.Fprintf(&sb, "  coded_height: %s\n", info.CodedHeight)
	_, _ = fmt.Fprintf(&sb, "  conformance_window: %s\n", info.ConformanceWindow)
	_, _ = fmt.Fprintf(&sb, "  pixel_aspect_ratio: %s\n", info.PixelAspectRatio)
	_, _ = fmt.Fprintf(&sb, "  level: %s\n", info.Level)
	_, _ = fmt.Fprintf(&sb, "  profile: %s\n", info.Profile)
	_, _ = fmt.Fprintf(&sb, "  chroma_format: %s\n", info.ChromaFormat)
	_, _ = fmt.Fprintf(&sb, "  nalu_length_size: %s\n", info.NALULengthSize)
	_, _ = fmt.Fprintf(&sb, "  colr_type: %s\n", info.ColrType)
	_, _ = fmt.Fprintf(&sb, "  vui_present: %s\n", info.VUIPresent)
	_, _ = fmt.Fprintf(&sb, "  video_signal_type_present: %s\n", info.VideoSignalTypePresent)
	_, _ = fmt.Fprintf(&sb, "  colour_description_present: %s\n", info.ColourDescription)
	_, _ = fmt.Fprintf(&sb, "  bit_depth: %s\n", info.BitDepth)
	_, _ = fmt.Fprintf(&sb, "  color_primaries: %s\n", info.ColorPrimaries)
	_, _ = fmt.Fprintf(&sb, "  transfer_characteristics: %s\n", info.TransferCharacteristics)
	_, _ = fmt.Fprintf(&sb, "  matrix_coefficients: %s\n", info.MatrixCoefficients)
	_, _ = fmt.Fprintf(&sb, "  color_range: %s\n", info.ColorRange)
	_, _ = fmt.Fprintf(&sb, "  mastering_display: %s\n", info.MasteringDisplay)
	_, _ = fmt.Fprintf(&sb, "  max_cll_fall: %s\n", info.MaxCLLFALL)
	_, _ = fmt.Fprintf(&sb, "  max_luma: %s\n", info.MaxLuma)
	_, _ = fmt.Fprintf(&sb, "  min_luma: %s\n", info.MinLuma)
	return strings.TrimRight(sb.String(), "\n")
}

func (h *HDRInfo) Report(includeInfo bool) HDRReport {
	report := HDRReport{
		HDR: HDRReportSection{
			Checks: make([]HDRReportCheck, 0, len(h.Checks)),
		},
	}
	if h.TrackID != 0 || h.CodecID != "" {
		report.HDR.Track = &HDRTrackInfo{
			ID:    h.TrackID,
			Codec: h.CodecID,
		}
	}
	for _, check := range h.Checks {
		status := "FAIL"
		if check.OK {
			status = "OK"
		}
		report.HDR.Checks = append(report.HDR.Checks, HDRReportCheck{
			Name:   check.Name,
			OK:     check.OK,
			Status: status,
			Detail: check.Detail,
		})
	}
	if includeInfo {
		info := h.FieldInfo()
		report.Info = &info
	}
	return report
}

func (h *HDRInfo) FieldInfo() HDRFieldInfo {
	const na = "na"
	info := HDRFieldInfo{
		Codec:                   na,
		Width:                   na,
		Height:                  na,
		AspectRatio:             na,
		SampleEntryWidth:        na,
		SampleEntryHeight:       na,
		TrackWidth:              na,
		TrackHeight:             na,
		SPSDisplayWidth:         na,
		SPSDisplayHeight:        na,
		CodedWidth:              na,
		CodedHeight:             na,
		ConformanceWindow:       na,
		PixelAspectRatio:        na,
		Level:                   na,
		Profile:                 na,
		ChromaFormat:            na,
		NALULengthSize:          na,
		ColrType:                na,
		VUIPresent:              na,
		VideoSignalTypePresent:  na,
		ColourDescription:       na,
		BitDepth:                na,
		ColorPrimaries:          na,
		TransferCharacteristics: na,
		MatrixCoefficients:      na,
		ColorRange:              na,
		MasteringDisplay:        na,
		MaxCLLFALL:              na,
		MaxLuma:                 na,
		MinLuma:                 na,
	}

	if h.CodecID != "" {
		info.Codec = h.CodecID
	}
	if h.Video.Width != 0 {
		info.Width = fmt.Sprintf("%d", h.Video.Width)
	}
	if h.Video.Height != 0 {
		info.Height = fmt.Sprintf("%d", h.Video.Height)
	}
	if h.Video.SampleEntryWidth != 0 {
		info.SampleEntryWidth = fmt.Sprintf("%d", h.Video.SampleEntryWidth)
	}
	if h.Video.SampleEntryHeight != 0 {
		info.SampleEntryHeight = fmt.Sprintf("%d", h.Video.SampleEntryHeight)
	}
	if h.Video.TrackWidth != 0 {
		info.TrackWidth = fmt.Sprintf("%d", h.Video.TrackWidth)
	}
	if h.Video.TrackHeight != 0 {
		info.TrackHeight = fmt.Sprintf("%d", h.Video.TrackHeight)
	}
	if h.Video.SPSDisplayWidth != 0 {
		info.SPSDisplayWidth = fmt.Sprintf("%d", h.Video.SPSDisplayWidth)
	}
	if h.Video.SPSDisplayHeight != 0 {
		info.SPSDisplayHeight = fmt.Sprintf("%d", h.Video.SPSDisplayHeight)
	}
	if h.Video.Width != 0 && h.Video.Height != 0 {
		aspectH, aspectV := h.Video.PixelAspectH, h.Video.PixelAspectV
		if aspectH == 0 || aspectV == 0 {
			aspectH, aspectV = 1, 1
		}
		aspectRatio := float64(h.Video.Width) * float64(aspectH) / (float64(h.Video.Height) * float64(aspectV))
		info.AspectRatio = fmt.Sprintf("%.6f", aspectRatio)
	}
	if h.Video.CodedWidth != 0 {
		info.CodedWidth = fmt.Sprintf("%d", h.Video.CodedWidth)
	}
	if h.Video.CodedHeight != 0 {
		info.CodedHeight = fmt.Sprintf("%d", h.Video.CodedHeight)
	}
	if h.Video.ConformanceWindowPresent {
		info.ConformanceWindow = fmt.Sprintf("left=%d right=%d top=%d bottom=%d",
			h.Video.ConformanceWindowLeft,
			h.Video.ConformanceWindowRight,
			h.Video.ConformanceWindowTop,
			h.Video.ConformanceWindowBottom)
	}
	if h.Video.PixelAspectH != 0 && h.Video.PixelAspectV != 0 {
		info.PixelAspectRatio = fmt.Sprintf("%d:%d", h.Video.PixelAspectH, h.Video.PixelAspectV)
	}
	levelIDC := h.HvcC.LevelIDC
	tierFlag := h.HvcC.TierFlag
	if h.HvcC.SPSLevelIDC != 0 {
		levelIDC = h.HvcC.SPSLevelIDC
		tierFlag = h.HvcC.SPSTierFlag
	}
	if levelIDC != 0 {
		info.Level = levelName(levelIDC, tierFlag)
	}
	profileIDC := h.HvcC.ProfileIDC
	if h.HvcC.SPSProfileIDC != 0 {
		profileIDC = h.HvcC.SPSProfileIDC
	}
	if profileIDC != 0 {
		info.Profile = profileName(profileIDC)
	}
	chromaFormatIDC := h.HvcC.ChromaFormatIDC
	if h.HvcC.SPSPresent {
		chromaFormatIDC = h.HvcC.SPSChromaFormatIDC
	}
	if h.HvcC.Present || h.HvcC.SPSPresent {
		info.ChromaFormat = chromaFormatName(chromaFormatIDC)
	}
	if h.HvcC.NALULengthSize != 0 {
		info.NALULengthSize = fmt.Sprintf("%d", h.HvcC.NALULengthSize)
	}
	if h.Colr.Present {
		info.ColrType = h.Colr.ColorType
	}
	if h.HvcC.SPSPresent {
		info.VUIPresent = fmt.Sprintf("%t", h.HvcC.VUIPresent)
		info.VideoSignalTypePresent = fmt.Sprintf("%t", h.HvcC.VideoSignalTypePresent)
		info.ColourDescription = fmt.Sprintf("%t", h.HvcC.ColourDescription)
	}
	bitDepthLuma := h.HvcC.BitDepthLuma
	bitDepthChroma := h.HvcC.BitDepthChroma
	if h.HvcC.SPSBitDepthLuma != 0 || h.HvcC.SPSBitDepthChroma != 0 {
		bitDepthLuma = h.HvcC.SPSBitDepthLuma
		bitDepthChroma = h.HvcC.SPSBitDepthChroma
	}
	if bitDepthLuma != 0 || bitDepthChroma != 0 {
		info.BitDepth = fmt.Sprintf("%d/%d", bitDepthLuma, bitDepthChroma)
	}

	if h.Colr.Present {
		info.ColorPrimaries = colourPrimariesName(h.Colr.ColorPrimaries)
		info.TransferCharacteristics = transferName(h.Colr.TransferCharacteristics)
		info.MatrixCoefficients = matrixName(h.Colr.MatrixCoefficients)
		if h.Colr.ColorType == mp4.ColorTypeOnScreenColors {
			info.ColorRange = rangeName(h.Colr.FullRangeFlag)
		}
	} else if h.HvcC.VUIPresent && h.HvcC.ColourDescription {
		info.ColorPrimaries = colourPrimariesName(uint16(h.HvcC.ColourPrimaries))
		info.TransferCharacteristics = transferName(uint16(h.HvcC.TransferCharacteristics))
		info.MatrixCoefficients = matrixName(uint16(h.HvcC.MatrixCoefficients))
	}
	if info.ColorRange == na && h.HvcC.VUIPresent && h.HvcC.VideoSignalTypePresent {
		info.ColorRange = rangeName(h.HvcC.VideoFullRangeFlag)
	}

	if m := h.masteringDisplay(); m != nil {
		info.MasteringDisplay = formatMdcv(*m)
	}
	if c := h.contentLightLevel(); c != nil {
		info.MaxCLLFALL = formatClli(*c)
	}
	if h.SEI.MaxLuma != "" {
		info.MaxLuma = h.SEI.MaxLuma
	}
	if h.SEI.MinLuma != "" {
		info.MinLuma = h.SEI.MinLuma
	}
	return info
}

func (h *HDRInfo) addCheck(name string, ok bool, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	h.Checks = append(h.Checks, HDRCheck{Name: name, OK: ok, Detail: detail})
	if !ok {
		h.Errors = append(h.Errors, fmt.Sprintf("%s: %s", name, detail))
	}
}

func (h *HDRInfo) addSEIError(format string, args ...any) {
	h.SEI.ParseErrors = appendLimited(h.SEI.ParseErrors, fmt.Sprintf(format, args...), 5)
}

func (h *HDRInfo) setVideoInfo(trak *mp4.TrakBox, vse *mp4.VisualSampleEntryBox, sps *hevc.SPS) {
	if vse != nil {
		h.Video.SampleEntryWidth = uint32(vse.Width)
		h.Video.SampleEntryHeight = uint32(vse.Height)
		h.Video.Width = h.Video.SampleEntryWidth
		h.Video.Height = h.Video.SampleEntryHeight
		if vse.Pasp != nil {
			h.Video.PixelAspectH = vse.Pasp.HSpacing
			h.Video.PixelAspectV = vse.Pasp.VSpacing
		}
	}
	if trak != nil && trak.Tkhd != nil {
		h.Video.TrackWidth = uint32(trak.Tkhd.Width) >> 16
		h.Video.TrackHeight = uint32(trak.Tkhd.Height) >> 16
		if h.Video.Width == 0 {
			h.Video.Width = h.Video.TrackWidth
		}
		if h.Video.Height == 0 {
			h.Video.Height = h.Video.TrackHeight
		}
	}
	if sps != nil {
		h.Video.CodedWidth = sps.PicWidthInLumaSamples
		h.Video.CodedHeight = sps.PicHeightInLumaSamples
		displayWidth, displayHeight := sps.ImageSize()
		h.Video.SPSDisplayWidth = displayWidth
		h.Video.SPSDisplayHeight = displayHeight
		h.Video.ConformanceWindowPresent = true
		h.Video.ConformanceWindowLeft = sps.ConformanceWindow.LeftOffset
		h.Video.ConformanceWindowRight = sps.ConformanceWindow.RightOffset
		h.Video.ConformanceWindowTop = sps.ConformanceWindow.TopOffset
		h.Video.ConformanceWindowBottom = sps.ConformanceWindow.BottomOffset
		if (h.Video.PixelAspectH == 0 || h.Video.PixelAspectV == 0) && sps.VUI != nil &&
			sps.VUI.SampleAspectRatioWidth != 0 && sps.VUI.SampleAspectRatioHeight != 0 {
			h.Video.PixelAspectH = uint32(sps.VUI.SampleAspectRatioWidth)
			h.Video.PixelAspectV = uint32(sps.VUI.SampleAspectRatioHeight)
		}
		if h.Video.Width == 0 {
			h.Video.Width = displayWidth
		}
		if h.Video.Height == 0 {
			h.Video.Height = displayHeight
		}
	}
}

func (h *HDRInfo) validateHvcC(vse *mp4.VisualSampleEntryBox) *hevc.SPS {
	if vse.HvcC == nil {
		h.addCheck("hvcC/SPS", false, "missing hvcC box")
		return nil
	}

	hvcC := vse.HvcC
	info := HDRHvcCInfo{
		Present:         true,
		ProfileIDC:      hvcC.GeneralProfileIDC,
		TierFlag:        hvcC.GeneralTierFlag,
		LevelIDC:        hvcC.GeneralLevelIDC,
		ChromaFormatIDC: hvcC.ChromaFormatIDC,
		BitDepthLuma:    hvcC.BitDepthLumaMinus8 + 8,
		BitDepthChroma:  hvcC.BitDepthChromaMinus8 + 8,
		NALULengthSize:  hvcC.LengthSizeMinusOne + 1,
	}

	var sps *hevc.SPS
	spsNalus := hvcC.GetNalusForType(hevc.NALU_SPS)
	if len(spsNalus) > 0 {
		info.SPSPresent = true
		parsedSPS, err := hevc.ParseSPSNALUnit(spsNalus[0])
		if err != nil {
			h.HvcC = info
			h.addCheck("hvcC/SPS", false, "SPS parse error: %v", err)
			return nil
		}
		sps = parsedSPS
		info.SPSProfileIDC = sps.ProfileTierLevel.GeneralProfileIDC
		info.SPSTierFlag = sps.ProfileTierLevel.GeneralTierFlag
		info.SPSLevelIDC = sps.ProfileTierLevel.GeneralLevelIDC
		info.SPSChromaFormatIDC = sps.ChromaFormatIDC
		info.SPSBitDepthLuma = sps.BitDepthLumaMinus8 + 8
		info.SPSBitDepthChroma = sps.BitDepthChromaMinus8 + 8
		if sps.VUI != nil {
			info.VUIPresent = true
			info.SampleAspectRatioWidth = sps.VUI.SampleAspectRatioWidth
			info.SampleAspectRatioHeight = sps.VUI.SampleAspectRatioHeight
			info.VideoSignalTypePresent = sps.VUI.VideoSignalTypePresentFlag
			info.ColourDescription = sps.VUI.ColourDescriptionFlag
			info.VideoFullRangeFlag = sps.VUI.VideoFullRangeFlag
			info.ColourPrimaries = sps.VUI.ColourPrimaries
			info.TransferCharacteristics = sps.VUI.TransferCharacteristics
			info.MatrixCoefficients = sps.VUI.MatrixCoefficients
		}
	}
	h.HvcC = info

	ok := info.ProfileIDC == hdr10Profile &&
		info.BitDepthLuma == 10 &&
		info.BitDepthChroma == 10 &&
		info.SPSPresent &&
		info.SPSProfileIDC == hdr10Profile &&
		info.SPSBitDepthLuma == 10 &&
		info.SPSBitDepthChroma == 10 &&
		info.VUIPresent &&
		info.ColourDescription &&
		!info.VideoFullRangeFlag &&
		info.ColourPrimaries == hdr10Primaries &&
		info.TransferCharacteristics == hdr10Transfer &&
		info.MatrixCoefficients == hdr10Matrix

	detail := fmt.Sprintf(
		"hvcC profile=%s bitDepth=%d/%d nalLength=%d; SPS profile=%s bitDepth=%d/%d VUI=%s/%s/%s range=%s",
		profileName(info.ProfileIDC), info.BitDepthLuma, info.BitDepthChroma, info.NALULengthSize,
		profileName(info.SPSProfileIDC), info.SPSBitDepthLuma, info.SPSBitDepthChroma,
		colourPrimariesName(uint16(info.ColourPrimaries)),
		transferName(uint16(info.TransferCharacteristics)),
		matrixName(uint16(info.MatrixCoefficients)),
		rangeName(info.VideoFullRangeFlag),
	)
	if !info.SPSPresent {
		detail = fmt.Sprintf("%s; missing SPS in hvcC", detail)
	} else if !info.VUIPresent {
		detail = fmt.Sprintf("%s; missing SPS VUI", detail)
	} else if !info.ColourDescription {
		detail = fmt.Sprintf("%s; missing SPS colour_description", detail)
	}
	h.addCheck("hvcC/SPS", ok, "%s", detail)
	return sps
}

func (h *HDRInfo) validateColr(vse *mp4.VisualSampleEntryBox) {
	colr := findColr(vse)
	if colr == nil {
		h.addCheck("colr", false, "missing colr box")
		return
	}

	h.Colr = HDRColrInfo{
		Present:                 true,
		ColorType:               colr.ColorType,
		ColorPrimaries:          colr.ColorPrimaries,
		TransferCharacteristics: colr.TransferCharacteristics,
		MatrixCoefficients:      colr.MatrixCoefficients,
		FullRangeFlag:           colr.FullRangeFlag,
	}
	ok := colr.ColorType == mp4.ColorTypeOnScreenColors &&
		colr.ColorPrimaries == hdr10Primaries &&
		colr.TransferCharacteristics == hdr10Transfer &&
		colr.MatrixCoefficients == hdr10Matrix &&
		!colr.FullRangeFlag
	h.addCheck("colr", ok, "type=%s primaries=%s transfer=%s matrix=%s range=%s",
		colr.ColorType,
		colourPrimariesName(colr.ColorPrimaries),
		transferName(colr.TransferCharacteristics),
		matrixName(colr.MatrixCoefficients),
		rangeName(colr.FullRangeFlag),
	)
}

func (h *HDRInfo) validateClli(vse *mp4.VisualSampleEntryBox) {
	if vse.Clli == nil {
		h.addCheck("clli", false, "missing clli box")
		return
	}
	h.Clli = clliFromBox(vse.Clli)
	h.addCheck("clli", true, "maxCLL=%d maxFALL=%d",
		h.Clli.MaxContentLightLevel, h.Clli.MaxPicAverageLightLevel)
}

func (h *HDRInfo) validateMdcv(vse *mp4.VisualSampleEntryBox) {
	if vse.Mdcv == nil {
		h.addCheck("mdcv", false, "missing mdcv box")
		return
	}
	h.Mdcv = mdcvFromBox(vse.Mdcv)
	h.addCheck("mdcv", true, "%s", formatMdcv(h.Mdcv))
}

func (h *HDRInfo) validateSEI(file *mp4.File, moov *mp4.MoovBox, trak *mp4.TrakBox, vse *mp4.VisualSampleEntryBox, sps *hevc.SPS) {
	if vse.HvcC != nil {
		h.observeSEINalus("hvcC", sps, vse.HvcC.GetNalusForType(hevc.NALU_SEI_PREFIX))
		h.observeSEINalus("hvcC", sps, vse.HvcC.GetNalusForType(hevc.NALU_SEI_SUFFIX))
	}

	lengthSize := 4
	if vse.HvcC != nil {
		lengthSize = int(vse.HvcC.LengthSizeMinusOne) + 1
	}
	samplesScanned, err := h.scanMediaSamples(file, moov, trak, lengthSize, sps)
	h.SEI.SamplesScanned = samplesScanned
	if err != nil {
		h.addSEIError("sample scan: %v", err)
	}

	hasMastering := h.SEI.Mastering != nil
	hasContent := h.SEI.ContentLightLevel != nil
	mdcvMatches := true
	clliMatches := true
	if hasMastering && h.Mdcv.Present {
		mdcvMatches = mdcvEqual(*h.SEI.Mastering, h.Mdcv)
	}
	if hasContent && h.Clli.Present {
		clliMatches = clliEqual(*h.SEI.ContentLightLevel, h.Clli)
	}

	ok := hasMastering && hasContent && mdcvMatches && clliMatches && len(h.SEI.ParseErrors) == 0
	var parts []string
	if hasMastering {
		parts = append(parts, fmt.Sprintf("MDCV %s from %s", formatMdcv(*h.SEI.Mastering), strings.Join(h.SEI.MasteringSources, ",")))
	} else {
		parts = append(parts, "missing MDCV SEI")
	}
	if hasContent {
		parts = append(parts, fmt.Sprintf("CLLI maxCLL=%d maxFALL=%d from %s",
			h.SEI.ContentLightLevel.MaxContentLightLevel,
			h.SEI.ContentLightLevel.MaxPicAverageLightLevel,
			strings.Join(h.SEI.ContentLightSources, ",")))
	} else {
		parts = append(parts, "missing CLLI SEI")
	}
	parts = append(parts, fmt.Sprintf("samplesScanned=%d sampleScanLimit=%ds nalusScanned=%d",
		h.SEI.SamplesScanned, hdrSEISampleScanLimitSeconds, h.SEI.NALUsScanned))
	if !mdcvMatches {
		parts = append(parts, "MDCV SEI does not match mdcv box")
	}
	if !clliMatches {
		parts = append(parts, "CLLI SEI does not match clli box")
	}
	if len(h.SEI.ParseErrors) > 0 {
		parts = append(parts, "parseErrors="+strings.Join(h.SEI.ParseErrors, "; "))
	}
	h.addCheck("HEVC SEI", ok, "%s", strings.Join(parts, "; "))
}

func (h *HDRInfo) scanMediaSamples(file *mp4.File, moov *mp4.MoovBox, trak *mp4.TrakBox, lengthSize int, sps *hevc.SPS) (int, error) {
	if lengthSize < 1 || lengthSize > 4 {
		return 0, fmt.Errorf("unsupported NALU length size %d", lengthSize)
	}
	scanLimit, hasScanLimit := hdrSampleScanLimitTicks(trak)
	if file.IsFragmented() {
		return h.scanFragmentedSamples(file, moov, trak, lengthSize, sps, scanLimit, hasScanLimit)
	}
	return 0, nil
}

func (h *HDRInfo) scanFragmentedSamples(file *mp4.File, moov *mp4.MoovBox, trak *mp4.TrakBox, lengthSize int, sps *hevc.SPS, scanLimit uint64, hasScanLimit bool) (int, error) {
	if len(file.Segments) == 0 {
		return 0, nil
	}

	var trex *mp4.TrexBox
	if moov != nil && moov.Mvex != nil && trak.Tkhd != nil {
		trex, _ = moov.Mvex.GetTrex(trak.Tkhd.TrackID)
	}
	if trex == nil {
		if trak != nil && trak.Tkhd != nil {
			return 0, fmt.Errorf("missing trex for video track %d", trak.Tkhd.TrackID)
		}
		return 0, fmt.Errorf("missing trex for video track")
	}

	var samplesScanned int
	var firstDecodeTime uint64
	var haveFirstDecodeTime bool
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(trex)
			if err != nil {
				return samplesScanned, err
			}
			for _, sample := range samples {
				if !haveFirstDecodeTime {
					firstDecodeTime = sample.DecodeTime
					haveFirstDecodeTime = true
				}
				if hasScanLimit && !withinHDRSampleScanLimit(sample.DecodeTime, firstDecodeTime, scanLimit) {
					return samplesScanned, nil
				}
				samplesScanned++
				h.observeSample("sample", sps, sample.Data, lengthSize)
			}
		}
	}
	return samplesScanned, nil
}

func hdrSampleScanLimitTicks(trak *mp4.TrakBox) (uint64, bool) {
	if trak == nil || trak.Mdia == nil || trak.Mdia.Mdhd == nil || trak.Mdia.Mdhd.Timescale == 0 {
		return 0, false
	}
	return uint64(trak.Mdia.Mdhd.Timescale) * hdrSEISampleScanLimitSeconds, true
}

func withinHDRSampleScanLimit(decodeTime, firstDecodeTime, scanLimit uint64) bool {
	if scanLimit == 0 || decodeTime < firstDecodeTime {
		return true
	}
	return decodeTime-firstDecodeTime < scanLimit
}

func (h *HDRInfo) observeSample(source string, sps *hevc.SPS, data []byte, lengthSize int) {
	nalus, err := nalusFromSample(data, lengthSize)
	if err != nil {
		h.addSEIError("%s: %v", source, err)
		return
	}
	h.observeSEINalus(source, sps, nalus)
}

func (h *HDRInfo) observeSEINalus(source string, sps *hevc.SPS, nalus [][]byte) {
	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		h.SEI.NALUsScanned++
		naluType := hevc.GetNaluType(nalu[0])
		if naluType != hevc.NALU_SEI_PREFIX && naluType != hevc.NALU_SEI_SUFFIX {
			continue
		}
		msgs, err := hevc.ParseSEINalu(nalu, sps)
		if err != nil && !errors.Is(err, sei.ErrRbspTrailingBitsMissing) {
			h.addSEIError("%s: %v", source, err)
			continue
		}
		for _, msg := range msgs {
			switch m := msg.(type) {
			case *sei.MasteringDisplayColourVolumeSEI:
				if h.SEI.Mastering == nil {
					mdcv := mdcvFromSEI(m)
					h.SEI.Mastering = &mdcv
				}
				h.SEI.MasteringSources = addUnique(h.SEI.MasteringSources, source)
			case *sei.ContentLightLevelInformationSEI:
				if h.SEI.ContentLightLevel == nil {
					clli := clliFromSEI(m)
					h.SEI.ContentLightLevel = &clli
				}
				h.SEI.ContentLightSources = addUnique(h.SEI.ContentLightSources, source)
			case *sei.UnregisteredSEI:
				h.observeUnregisteredSEI(source, m)
			}
		}
	}
}

func (h *HDRInfo) observeUnregisteredSEI(source string, msg *sei.UnregisteredSEI) {
	payload := msg.Payload()
	if len(payload) <= 16 {
		return
	}
	settings := strings.TrimRight(string(payload[16:]), "\x00")
	if !strings.Contains(settings, "x265") || !strings.Contains(settings, "options:") {
		return
	}
	if h.SEI.EncodingSettings == "" {
		h.SEI.EncodingSettings = settings
	}
	h.SEI.EncodingSettingsSources = addUnique(h.SEI.EncodingSettingsSources, source)
	if h.SEI.MinLuma == "" {
		if value, ok := x265EncodingSetting(settings, "min-luma"); ok {
			h.SEI.MinLuma = value
		}
	}
	if h.SEI.MaxLuma == "" {
		if value, ok := x265EncodingSetting(settings, "max-luma"); ok {
			h.SEI.MaxLuma = value
		}
	}
}

func findHEVCVideoSampleEntry(file *mp4.File) (*mp4.MoovBox, *mp4.TrakBox, *mp4.VisualSampleEntryBox, bool) {
	moov := file.Moov
	if file.Init != nil && file.Init.Moov != nil {
		moov = file.Init.Moov
	}
	if moov == nil {
		return nil, nil, nil, false
	}
	for _, trak := range moov.Traks {
		stbl := sampleTable(trak)
		if stbl == nil || stbl.Stsd == nil {
			continue
		}
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType != "vide" {
			continue
		}
		if stbl.Stsd.HvcX != nil {
			return moov, trak, stbl.Stsd.HvcX, true
		}
		if stbl.Stsd.Encv != nil && stbl.Stsd.Encv.HvcC != nil {
			return moov, trak, stbl.Stsd.Encv, true
		}
	}
	return nil, nil, nil, false
}

func sampleTable(trak *mp4.TrakBox) *mp4.StblBox {
	if trak == nil || trak.Mdia == nil || trak.Mdia.Minf == nil {
		return nil
	}
	return trak.Mdia.Minf.Stbl
}

func findColr(vse *mp4.VisualSampleEntryBox) *mp4.ColrBox {
	for _, child := range vse.Children {
		if colr, ok := child.(*mp4.ColrBox); ok {
			return colr
		}
	}
	return nil
}

func nalusFromSample(sample []byte, lengthSize int) ([][]byte, error) {
	if len(sample) < lengthSize {
		return nil, fmt.Errorf("less than %d bytes, no NALUs", lengthSize)
	}
	nalus := make([][]byte, 0, 2)
	pos := 0
	for pos < len(sample) {
		if pos+lengthSize > len(sample) {
			return nil, fmt.Errorf("truncated NALU length field")
		}
		naluLength := 0
		for i := 0; i < lengthSize; i++ {
			naluLength = (naluLength << 8) | int(sample[pos+i])
		}
		pos += lengthSize
		if naluLength == 0 {
			return nil, fmt.Errorf("zero-length NALU")
		}
		if pos+naluLength > len(sample) {
			return nil, fmt.Errorf("NALU length fields are bad")
		}
		nalus = append(nalus, sample[pos:pos+naluLength])
		pos += naluLength
	}
	return nalus, nil
}

func clliFromBox(box *mp4.ClliBox) HDRClliInfo {
	return HDRClliInfo{
		Present:                 true,
		MaxContentLightLevel:    box.MaxContentLightLevel,
		MaxPicAverageLightLevel: box.MaxPicAverageLightLevel,
	}
}

func clliFromSEI(msg *sei.ContentLightLevelInformationSEI) HDRClliInfo {
	return HDRClliInfo{
		Present:                 true,
		MaxContentLightLevel:    msg.MaxContentLightLevel,
		MaxPicAverageLightLevel: msg.MaxPicAverageLightLevel,
	}
}

func mdcvFromBox(box *mp4.MdcvBox) HDRMdcvInfo {
	return HDRMdcvInfo{
		Present:                      true,
		DisplayPrimariesX:            box.DisplayPrimariesX,
		DisplayPrimariesY:            box.DisplayPrimariesY,
		WhitePointX:                  box.WhitePointX,
		WhitePointY:                  box.WhitePointY,
		MaxDisplayMasteringLuminance: box.MaxDisplayMasteringLuminance,
		MinDisplayMasteringLuminance: box.MinDisplayMasteringLuminance,
	}
}

func mdcvFromSEI(msg *sei.MasteringDisplayColourVolumeSEI) HDRMdcvInfo {
	return HDRMdcvInfo{
		Present:                      true,
		DisplayPrimariesX:            msg.DisplayPrimariesX,
		DisplayPrimariesY:            msg.DisplayPrimariesY,
		WhitePointX:                  msg.WhitePointX,
		WhitePointY:                  msg.WhitePointY,
		MaxDisplayMasteringLuminance: msg.MaxDisplayMasteringLuminance,
		MinDisplayMasteringLuminance: msg.MinDisplayMasteringLuminance,
	}
}

func clliEqual(a, b HDRClliInfo) bool {
	return a.MaxContentLightLevel == b.MaxContentLightLevel &&
		a.MaxPicAverageLightLevel == b.MaxPicAverageLightLevel
}

func mdcvEqual(a, b HDRMdcvInfo) bool {
	return a.DisplayPrimariesX == b.DisplayPrimariesX &&
		a.DisplayPrimariesY == b.DisplayPrimariesY &&
		a.WhitePointX == b.WhitePointX &&
		a.WhitePointY == b.WhitePointY &&
		a.MaxDisplayMasteringLuminance == b.MaxDisplayMasteringLuminance &&
		a.MinDisplayMasteringLuminance == b.MinDisplayMasteringLuminance
}

func formatMdcv(m HDRMdcvInfo) string {
	return fmt.Sprintf("G(%d,%d)B(%d,%d)R(%d,%d)WP(%d,%d)L(%d,%d)",
		m.DisplayPrimariesX[0], m.DisplayPrimariesY[0],
		m.DisplayPrimariesX[1], m.DisplayPrimariesY[1],
		m.DisplayPrimariesX[2], m.DisplayPrimariesY[2],
		m.WhitePointX, m.WhitePointY,
		m.MaxDisplayMasteringLuminance, m.MinDisplayMasteringLuminance)
}

func formatClli(c HDRClliInfo) string {
	return fmt.Sprintf("%d,%d", c.MaxContentLightLevel, c.MaxPicAverageLightLevel)
}

func x265EncodingSetting(settings, key string) (string, bool) {
	for _, field := range strings.Fields(settings) {
		field = strings.Trim(field, "/")
		if value, ok := strings.CutPrefix(field, key+"="); ok {
			return value, true
		}
	}
	return "", false
}

func (h *HDRInfo) masteringDisplay() *HDRMdcvInfo {
	if h.Mdcv.Present {
		return &h.Mdcv
	}
	return h.SEI.Mastering
}

func (h *HDRInfo) contentLightLevel() *HDRClliInfo {
	if h.Clli.Present {
		return &h.Clli
	}
	return h.SEI.ContentLightLevel
}

func profileName(profile byte) string {
	switch profile {
	case 1:
		return "Main(1)"
	case hdr10Profile:
		return "Main10(2)"
	case 3:
		return "Main Still Picture(3)"
	case 4:
		return "Range Extensions(4)"
	case 5:
		return "High Throughput(5)"
	case 6:
		return "Multiview Main(6)"
	case 0:
		return "unknown(0)"
	default:
		return fmt.Sprintf("%d", profile)
	}
}

func levelName(levelIDC byte, highTier bool) string {
	if levelIDC == 0 {
		return "na"
	}
	tier := "Main tier"
	if highTier {
		tier = "High tier"
	}
	major := int(levelIDC) / 30
	minor := (int(levelIDC) % 30) / 3
	return fmt.Sprintf("%s %d.%d(%d)", tier, major, minor, levelIDC)
}

func chromaFormatName(v byte) string {
	switch v {
	case 0:
		return "monochrome(0)"
	case 1:
		return "4:2:0(1)"
	case 2:
		return "4:2:2(2)"
	case 3:
		return "4:4:4(3)"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func colourPrimariesName(v uint16) string {
	switch v {
	case hdr10Primaries:
		return "BT.2020(9)"
	case 1:
		return "BT.709(1)"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func transferName(v uint16) string {
	switch v {
	case hdr10Transfer:
		return "PQ/ST2084(16)"
	case 1:
		return "BT.709(1)"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func matrixName(v uint16) string {
	switch v {
	case hdr10Matrix:
		return "BT.2020 non-constant(9)"
	case 1:
		return "BT.709(1)"
	default:
		return fmt.Sprintf("%d", v)
	}
}

func rangeName(fullRange bool) string {
	if fullRange {
		return "full"
	}
	return "limited"
}

func addUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendLimited(values []string, value string, limit int) []string {
	if len(values) >= limit {
		return values
	}
	return append(values, value)
}
