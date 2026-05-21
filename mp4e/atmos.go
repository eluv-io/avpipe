package mp4e

import (
	"fmt"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
)

// AtmosInfo holds Dolby Atmos signaling information extracted from an MP4
// container. Atmos can be carried via E-AC-3 + JOC (ETSI TS 103 420) or AC-4
// (ETSI TS 103 190-2).
type AtmosInfo struct {
	TrackID uint32
	CodecID string

	Audio AtmosAudioInfo
	EC3   AtmosEC3Info
	AC4   AtmosAC4Info

	Checks []AtmosCheck
	Errors []string
}

type AtmosAudioInfo struct {
	ChannelCount uint16
	SampleRate   uint32
}

type AtmosCheck struct {
	Name   string
	OK     bool
	Detail string
}

// AtmosEC3Info captures E-AC-3 (Dolby Digital Plus) specific fields. The JOC
// fields signal Dolby Atmos per ETSI TS 103 420.
type AtmosEC3Info struct {
	Present         bool
	DataRate        uint16
	NumSubstreams   int
	Substreams      []AtmosEC3Substream
	NrChannels      int
	ChanMap         uint16
	JOC             bool
	ComplexityIndex int
	ReservedBytes   int
}

type AtmosEC3Substream struct {
	FSCod     byte
	BSID      byte
	BSMod     byte
	ACMod     byte
	LFEOn     byte
	NumDepSub byte
	ChanLoc   uint16
}

// AtmosAC4Info captures AC-4 specific fields. Atmos / immersive content is
// signaled via presentation_version >= 1 in ac4_dsi_v1.
type AtmosAC4Info struct {
	Present          bool
	AC4DSIVersion    uint8
	BitstreamVersion uint8
	FSIndex          uint8
	SampleRate       int
	FrameRateIndex   uint8
	FrameRate        string
	NPresentations   uint16
	Presentations    []AtmosAC4Presentation
}

type AtmosAC4Presentation struct {
	Version uint8
}

type AtmosFieldInfo struct {
	Codec            string `json:"codec"`
	Channels         string `json:"channels"`
	SampleRate       string `json:"sample_rate"`
	DataRateKbps     string `json:"data_rate_kbps,omitempty"`
	Substreams       string `json:"substreams,omitempty"`
	ChanMap          string `json:"chan_map,omitempty"`
	ChanMapHex       string `json:"chan_map_hex,omitempty"`
	JOC              string `json:"joc,omitempty"`
	ComplexityIndex  string `json:"complexity_index,omitempty"`
	AC4DSIVersion    string `json:"ac4_dsi_version,omitempty"`
	AC4BitstreamVer  string `json:"ac4_bitstream_version,omitempty"`
	AC4FrameRate     string `json:"ac4_frame_rate,omitempty"`
	AC4Presentations string `json:"ac4_presentations,omitempty"`
}

type AtmosReport struct {
	Track  *AtmosTrackInfo    `json:"track,omitempty"`
	Checks []AtmosReportCheck `json:"checks"`
	Info   *AtmosFieldInfo    `json:"info,omitempty"`
}

type AtmosTrackInfo struct {
	ID    uint32 `json:"id,omitempty"`
	Codec string `json:"codec,omitempty"`
}

type AtmosReportCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func ValidateAtmos(file *mp4.File) (*AtmosInfo, error) {
	info := &AtmosInfo{}
	if file == nil {
		return info, fmt.Errorf("nil MP4 file")
	}

	trak, ase, ok := findAtmosCapableAudio(file)
	if !ok {
		info.addCheck("audio_track", false, "no Dolby Atmos-capable audio track (ec-3 or ac-4) found")
		info.addCheck("signaling", false, "no Atmos signaling box (dec3 or dac4) found")
		return info, nil
	}

	if trak.Tkhd != nil {
		info.TrackID = trak.Tkhd.TrackID
	}
	info.CodecID = ase.Type()
	info.Audio.ChannelCount = ase.ChannelCount
	info.Audio.SampleRate = uint32(ase.SampleRate)

	switch info.CodecID {
	case "ec-3":
		info.validateEC3(ase)
	case "ac-4":
		info.validateAC4(ase)
	}

	// EC-3 sample entry ChannelCount is a downmix indicator (typically 2 or 6),
	// not the actual channel count. Prefer the dec3-derived value.
	displayChannels := uint16(ase.ChannelCount)
	if info.EC3.Present && info.EC3.NrChannels > 0 {
		displayChannels = uint16(info.EC3.NrChannels)
		info.Audio.ChannelCount = displayChannels
	}

	// Prepend the audio_track check so it appears before codec-specific checks.
	track := AtmosCheck{
		Name: "audio_track",
		OK:   true,
		Detail: fmt.Sprintf("track id=%d codec=%s channels=%d",
			info.TrackID, info.CodecID, displayChannels),
	}
	info.Checks = append([]AtmosCheck{track}, info.Checks...)
	return info, nil
}

func findAtmosCapableAudio(file *mp4.File) (*mp4.TrakBox, *mp4.AudioSampleEntryBox, bool) {
	moov := file.Moov
	if file.Init != nil && file.Init.Moov != nil {
		moov = file.Init.Moov
	}
	if moov == nil {
		return nil, nil, false
	}
	for _, trak := range moov.Traks {
		stbl := sampleTable(trak)
		if stbl == nil || stbl.Stsd == nil {
			continue
		}
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType != "soun" {
			continue
		}
		for _, child := range stbl.Stsd.Children {
			ase, ok := child.(*mp4.AudioSampleEntryBox)
			if !ok {
				continue
			}
			t := ase.Type()
			if t == "ec-3" || t == "ac-4" {
				return trak, ase, true
			}
		}
	}
	return nil, nil, false
}

func (a *AtmosInfo) validateEC3(ase *mp4.AudioSampleEntryBox) {
	if ase.Dec3 == nil {
		a.addCheck("dec3", false, "missing dec3 box in ec-3 sample entry")
		a.addCheck("joc", false, "cannot evaluate JOC without dec3")
		return
	}
	d := ase.Dec3
	nrChannels, chanMap := d.ChannelInfo()
	ec3 := AtmosEC3Info{
		Present:       true,
		DataRate:      d.DataRate,
		NumSubstreams: len(d.EC3Subs),
		NrChannels:    nrChannels,
		ChanMap:       chanMap,
		ReservedBytes: len(d.Reserved),
	}
	for _, sub := range d.EC3Subs {
		ec3.Substreams = append(ec3.Substreams, AtmosEC3Substream{
			FSCod:     sub.FSCod,
			BSID:      sub.BSID,
			BSMod:     sub.BSMod,
			ACMod:     sub.ACMod,
			LFEOn:     sub.LFEOn,
			NumDepSub: sub.NumDepSub,
			ChanLoc:   sub.ChanLoc,
		})
	}

	// JOC extension (ETSI TS 103 420):
	//   bits[7:1] reserved (7 bits, all zero)
	//   bits[0]   flag_ec3_extension_type_a (1 bit, LSB of first byte)
	//   bits[15:8] complexity_index_type_a (8 bits, present iff flag==1)
	if len(d.Reserved) >= 1 && d.Reserved[0]&0x01 == 1 {
		ec3.JOC = true
		if len(d.Reserved) >= 2 {
			ec3.ComplexityIndex = int(d.Reserved[1])
		}
	}

	a.EC3 = ec3

	a.addCheck("dec3", true, "dataRate=%dkbps substreams=%d channels=%d chanmap=%04X",
		ec3.DataRate, ec3.NumSubstreams, ec3.NrChannels, ec3.ChanMap)

	if ec3.JOC {
		a.addCheck("joc", true, "flag_ec3_extension_type_a=1 complexity_index=%d",
			ec3.ComplexityIndex)
	} else {
		a.addCheck("joc", false, "flag_ec3_extension_type_a not set (reservedBytes=%d) - not Atmos",
			ec3.ReservedBytes)
	}
}

func (a *AtmosInfo) validateAC4(ase *mp4.AudioSampleEntryBox) {
	if ase.Dac4 == nil {
		a.addCheck("dac4", false, "missing dac4 box in ac-4 sample entry")
		a.addCheck("presentation", false, "cannot evaluate AC-4 presentations without dac4")
		return
	}
	d := ase.Dac4
	ac4 := AtmosAC4Info{
		Present:          true,
		AC4DSIVersion:    d.AC4DSIVersion,
		BitstreamVersion: d.BitstreamVersion,
		FSIndex:          d.FSIndex,
		SampleRate:       d.GetSamplingFrequency(),
		FrameRateIndex:   d.FrameRateIndex,
		FrameRate:        d.GetFrameRateString(),
		NPresentations:   d.NPresentations,
	}
	for _, pr := range d.Presentations {
		ac4.Presentations = append(ac4.Presentations, AtmosAC4Presentation{
			Version: pr.PresentationVersion,
		})
	}
	a.AC4 = ac4

	a.addCheck("dac4", true, "ac4DSIVersion=%d bitstreamVersion=%d frameRate=%s sampleRate=%d nPresentations=%d",
		ac4.AC4DSIVersion, ac4.BitstreamVersion, ac4.FrameRate, ac4.SampleRate, ac4.NPresentations)

	// AC-4 immersive (Atmos) requires at least one presentation parsed as
	// ac4_presentation_v1_dsi (or later), which carries object-based /
	// immersive signaling per ETSI TS 103 190-2.
	hasImmersive := false
	var versions []string
	for _, pr := range ac4.Presentations {
		versions = append(versions, fmt.Sprintf("%d", pr.Version))
		if pr.Version >= 1 {
			hasImmersive = true
		}
	}
	if hasImmersive {
		a.addCheck("presentation", true, "presentation_versions=[%s] - immersive (Atmos-capable)",
			strings.Join(versions, ","))
	} else if len(versions) > 0 {
		a.addCheck("presentation", false, "presentation_versions=[%s] - no immersive (v>=1) presentation, not Atmos",
			strings.Join(versions, ","))
	} else {
		a.addCheck("presentation", false, "no presentations parsed (declared=%d)", ac4.NPresentations)
	}
}

func (a *AtmosInfo) addCheck(name string, ok bool, format string, args ...any) {
	detail := fmt.Sprintf(format, args...)
	a.Checks = append(a.Checks, AtmosCheck{Name: name, OK: ok, Detail: detail})
	if !ok {
		a.Errors = append(a.Errors, fmt.Sprintf("%s: %s", name, detail))
	}
}

func (a *AtmosInfo) String() string {
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "atmos:\n")
	if a.TrackID != 0 || a.CodecID != "" {
		_, _ = fmt.Fprintf(&sb, "  track: id=%d codec=%s\n", a.TrackID, a.CodecID)
	}
	for _, check := range a.Checks {
		status := "FAIL"
		if check.OK {
			status = "OK"
		}
		_, _ = fmt.Fprintf(&sb, "  [%s] %s: %s\n", status, check.Name, check.Detail)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (a *AtmosInfo) InfoString() string {
	info := a.FieldInfo()
	var sb strings.Builder
	_, _ = fmt.Fprintf(&sb, "info:\n")
	_, _ = fmt.Fprintf(&sb, "  codec: %s\n", info.Codec)
	_, _ = fmt.Fprintf(&sb, "  channels: %s\n", info.Channels)
	_, _ = fmt.Fprintf(&sb, "  sample_rate: %s\n", info.SampleRate)
	if info.DataRateKbps != "" {
		_, _ = fmt.Fprintf(&sb, "  data_rate_kbps: %s\n", info.DataRateKbps)
	}
	if info.Substreams != "" {
		_, _ = fmt.Fprintf(&sb, "  substreams: %s\n", info.Substreams)
	}
	if info.ChanMap != "" {
		_, _ = fmt.Fprintf(&sb, "  chan_map: %s\n", info.ChanMap)
		_, _ = fmt.Fprintf(&sb, "  chan_map_hex: %s\n", info.ChanMapHex)
	}
	if info.JOC != "" {
		_, _ = fmt.Fprintf(&sb, "  joc: %s\n", info.JOC)
	}
	if info.ComplexityIndex != "" {
		_, _ = fmt.Fprintf(&sb, "  complexity_index: %s\n", info.ComplexityIndex)
	}
	if info.AC4DSIVersion != "" {
		_, _ = fmt.Fprintf(&sb, "  ac4_dsi_version: %s\n", info.AC4DSIVersion)
		_, _ = fmt.Fprintf(&sb, "  ac4_bitstream_version: %s\n", info.AC4BitstreamVer)
		_, _ = fmt.Fprintf(&sb, "  ac4_frame_rate: %s\n", info.AC4FrameRate)
		_, _ = fmt.Fprintf(&sb, "  ac4_presentations: %s\n", info.AC4Presentations)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (a *AtmosInfo) Report(includeInfo bool) AtmosReport {
	report := AtmosReport{
		Checks: make([]AtmosReportCheck, 0, len(a.Checks)),
	}
	if a.TrackID != 0 || a.CodecID != "" {
		report.Track = &AtmosTrackInfo{
			ID:    a.TrackID,
			Codec: a.CodecID,
		}
	}
	for _, check := range a.Checks {
		status := "FAIL"
		if check.OK {
			status = "OK"
		}
		report.Checks = append(report.Checks, AtmosReportCheck{
			Name:   check.Name,
			OK:     check.OK,
			Status: status,
			Detail: check.Detail,
		})
	}
	if includeInfo {
		info := a.FieldInfo()
		report.Info = &info
	}
	return report
}

func (a *AtmosInfo) FieldInfo() AtmosFieldInfo {
	const na = "na"
	info := AtmosFieldInfo{
		Codec:      na,
		Channels:   na,
		SampleRate: na,
	}
	if a.CodecID != "" {
		info.Codec = a.CodecID
	}
	if a.Audio.ChannelCount != 0 {
		info.Channels = fmt.Sprintf("%d", a.Audio.ChannelCount)
	}
	if a.Audio.SampleRate != 0 {
		info.SampleRate = fmt.Sprintf("%d", a.Audio.SampleRate)
	}

	if a.EC3.Present {
		info.DataRateKbps = fmt.Sprintf("%d", a.EC3.DataRate)
		info.Substreams = fmt.Sprintf("%d", a.EC3.NumSubstreams)
		info.ChanMap = ec3ChanMapString(a.EC3.ChanMap)
		info.ChanMapHex = fmt.Sprintf("%04X", a.EC3.ChanMap)
		if a.EC3.JOC {
			info.JOC = "true"
			info.ComplexityIndex = fmt.Sprintf("%d", a.EC3.ComplexityIndex)
		} else {
			info.JOC = "false"
		}
	}

	if a.AC4.Present {
		info.AC4DSIVersion = fmt.Sprintf("%d", a.AC4.AC4DSIVersion)
		info.AC4BitstreamVer = fmt.Sprintf("%d", a.AC4.BitstreamVersion)
		info.AC4FrameRate = a.AC4.FrameRate
		var versions []string
		for _, pr := range a.AC4.Presentations {
			versions = append(versions, fmt.Sprintf("v%d", pr.Version))
		}
		if len(versions) > 0 {
			info.AC4Presentations = strings.Join(versions, ",")
		} else {
			info.AC4Presentations = fmt.Sprintf("(%d declared, none parsed)", a.AC4.NPresentations)
		}
	}
	return info
}

func ec3ChanMapString(chanMap uint16) string {
	var names []string
	for i, name := range ec3ChanMapNames {
		if chanMap&(1<<(15-i)) != 0 {
			names = append(names, name)
		}
	}
	return strings.Join(names, " ")
}
