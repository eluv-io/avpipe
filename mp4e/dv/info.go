package dv

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

// InfoOptions controls optional verbosity for sample inspection.
type InfoOptions struct {
	ShowIDR bool
}

// DVConfigRecord holds the parsed contents of a dvcC or dvvC box payload.
// Layout per Dolby Vision ISOBMFF spec:
//
//	byte 0:   dv_version_major (8 bits)
//	byte 1:   dv_version_minor (8 bits)
//	bytes 2-3:
//	          dv_profile        (7 bits)
//	          dv_level          (6 bits)
//	          rpu_present_flag  (1 bit)
//	          el_present_flag   (1 bit)
//	          bl_present_flag   (1 bit)
//	byte 4:   bl_signal_compat_id (high nibble, bits [7:4])
type DVConfigRecord struct {
	DVVersionMajor          uint8
	DVVersionMinor          uint8
	DVProfile               uint8
	DVLevel                 uint8
	RPUPresentFlag          bool
	ELPresentFlag           bool
	BLPresentFlag           bool
	BLSignalCompatibilityID uint8
}

// parseDVConfig parses the raw payload of a dvcC or dvvC UnknownBox.
// The payload is the box body after the 8-byte box header has been stripped
// by mp4ff, so byte 0 is dv_version_major.
func parseDVConfig(payload []byte) (*DVConfigRecord, error) {
	if len(payload) < 4 {
		return nil, fmt.Errorf("dv config payload too short: %d bytes (need 4)", len(payload))
	}
	r := &DVConfigRecord{}
	r.DVVersionMajor = payload[0]
	r.DVVersionMinor = payload[1]

	// Bytes 2-3 packed (big-endian 16-bit word):
	// [15:9]  dv_profile        (7 bits)
	// [8:3]   dv_level          (6 bits)
	// [2]     rpu_present_flag
	// [1]     el_present_flag
	// [0]     bl_present_flag
	word := binary.BigEndian.Uint16(payload[2:4])
	r.DVProfile = uint8((word >> 9) & 0x7F)
	r.DVLevel = uint8((word >> 3) & 0x3F)
	r.RPUPresentFlag = (word>>2)&0x1 == 1
	r.ELPresentFlag = (word>>1)&0x1 == 1
	r.BLPresentFlag = word&0x1 == 1

	// byte 4: bl_signal_compat_id in the high nibble (bits [7:4])
	if len(payload) >= 5 {
		r.BLSignalCompatibilityID = (payload[4] >> 4) & 0x0F
	}
	return r, nil
}

// Info inspects an MP4 file for Dolby Vision correctness and QC,
// mirroring the structure of mvhevc.Info.
func Info(inputPath string, opts InfoOptions) (map[string]any, error) {
	ifd, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("could not open input file: %w", err)
	}
	defer func() { _ = ifd.Close() }()

	parsedMP4, err := mp4.DecodeFile(ifd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return nil, fmt.Errorf("could not decode MP4: %w", err)
	}

	if parsedMP4.Moov == nil {
		return nil, fmt.Errorf("no moov box found")
	}

	results := make(map[string]any)
	var dvTrackIDs []uint32

	for i, trak := range parsedMP4.Moov.Traks {
		key := fmt.Sprintf("Track %d (ID=%d)", i+1, trak.Tkhd.TrackID)
		track := map[string]any{
			"track_id": trak.Tkhd.TrackID,
		}

		stbl := trak.Mdia.Minf.Stbl
		if stbl == nil || stbl.Stsd == nil {
			results[key] = track
			continue
		}

		isDVTrack := false

		for _, child := range stbl.Stsd.Children {
			vse, ok := child.(*mp4.VisualSampleEntryBox)
			if !ok {
				continue
			}

			track["Sample entry"] = fmt.Sprintf("%s (%dx%d)", vse.Type(), vse.Width, vse.Height)

			// dvcC / dvvC land as UnknownBox children of the sample entry
			for _, vseChild := range vse.Children {
				ub, ok := vseChild.(*mp4.UnknownBox)
				if !ok {
					continue
				}
				boxType := ub.Type()
				if boxType != "dvcC" && boxType != "dvvC" {
					continue
				}
				isDVTrack = true
				rec, parseErr := parseDVConfig(ub.Payload())
				if parseErr != nil {
					track[fmt.Sprintf("%s (DV config)", boxType)] = map[string]any{
						"error": parseErr.Error(),
					}
					continue
				}
				infoKey := fmt.Sprintf("%s (DV config)", boxType)
				track[infoKey] = formatDVConfig(rec)
				track[infoKey+" QC"] = validateDVConfig(rec)
			}

			// Base codec configs
			if vse.HvcC != nil {
				track["hvcC (HEVC base layer)"] = printHvcCInfo(vse.HvcC.DecConfRec)
			}
			if vse.AvcC != nil {
				track["avcC (AVC base layer)"] = printAvcCInfo(vse.AvcC)
			}
			if vse.LhvC != nil {
				track["lhvC (enhancement layer)"] = printLhvCInfo(vse.LhvC.DecConfRec)
			}

			// Color metadata — ColrBox is not a named field in this fork;
			// find it by scanning Children.
			for _, vseChild := range vse.Children {
				if colr, ok := vseChild.(*mp4.ColrBox); ok {
					track["colr (Color info)"] = formatColrInfo(colr)
					track["colr QC"] = validateColrForDV(colr)
					break
				}
			}

			// Mastering display / CLL
			if vse.Mdcv != nil {
				track["mdcv (Mastering display)"] = formatMdcvInfo(vse.Mdcv)
			}
			if vse.Clli != nil {
				track["clli (Content light level)"] = formatClliInfo(vse.Clli)
			}

			// Spatial / VEXU
			if vse.Vexu != nil {
				track["vexu (Spatial Video)"] = printVexuInfo(vse.Vexu)
			}
		}

		if isDVTrack {
			dvTrackIDs = append(dvTrackIDs, trak.Tkhd.TrackID)
		}

		timeScale := trak.Mdia.Mdhd.Timescale
		trackID := trak.Tkhd.TrackID

		if parsedMP4.IsFragmented() {
			track["Samples Info"] = printFragmentedInfo(parsedMP4, trackID, timeScale, opts)
		} else {
			track["Samples Info"] = printUnfragmentedSampleInfo(trak, stbl, timeScale, opts)
		}

		if trak.Trgr != nil {
			track["trgr (Track Group)"] = printTrackGroupInfo(trak)
		}

		results[key] = track
	}

	results["File-level QC"] = fileLevelDVQC(parsedMP4, dvTrackIDs)
	return results, nil
}

// ── dvcC / dvvC formatting ────────────────────────────────────────────────────

func formatDVConfig(r *DVConfigRecord) map[string]any {
	return map[string]any{
		"DVVersionMajor":          r.DVVersionMajor,
		"DVVersionMinor":          r.DVVersionMinor,
		"Profile":                 fmt.Sprintf("%d (%s)", r.DVProfile, dvProfileName(r.DVProfile)),
		"Level":                   fmt.Sprintf("%d (%s)", r.DVLevel, dvLevelDesc(r.DVLevel)),
		"RPUPresentFlag":          r.RPUPresentFlag,
		"ELPresentFlag":           r.ELPresentFlag,
		"BLPresentFlag":           r.BLPresentFlag,
		"BLSignalCompatibilityID": fmt.Sprintf("%d (%s)", r.BLSignalCompatibilityID, dvCompatName(r.BLSignalCompatibilityID)),
	}
}

// knownDVProfiles is the set of all known Dolby Vision profile numbers.
var knownDVProfiles = map[uint8]bool{
	0: true, 1: true, 2: true, 3: true, 4: true,
	5: true, 6: true, 7: true, 8: true, 9: true,
	20: true, // MV-HEVC (spec v2.1)
}

func validateDVConfig(r *DVConfigRecord) map[string]any {
	var issues, notes []string

	if !knownDVProfiles[r.DVProfile] {
		issues = append(issues, fmt.Sprintf("unknown DV profile %d", r.DVProfile))
	}
	if r.DVLevel == 0 || r.DVLevel > 13 {
		issues = append(issues, fmt.Sprintf("DV level %d out of expected range [1-13]", r.DVLevel))
	}
	if !r.RPUPresentFlag {
		issues = append(issues, "RPUPresentFlag=false — RPU NALUs are required for all DV profiles")
	}

	switch r.DVProfile {
	case 5: // single-layer IPTPQc2
		if r.BLPresentFlag {
			issues = append(issues, "profile 5 (single-layer) must not have BLPresentFlag set")
		}
		if r.ELPresentFlag {
			issues = append(issues, "profile 5 (single-layer) must not have ELPresentFlag set")
		}
	case 8: // HEVC HDR10/SDR compat
		if r.BLSignalCompatibilityID != 1 && r.BLSignalCompatibilityID != 2 && r.BLSignalCompatibilityID != 4 {
			notes = append(notes, fmt.Sprintf("profile 8 typically uses BL compat ID 1 (HDR10), 2 (SDR), or 4 (HDR10+); got %d", r.BLSignalCompatibilityID))
		}
	case 9: // AVC base layer
		if r.ELPresentFlag {
			issues = append(issues, "profile 9 (AVC base) must not have ELPresentFlag set")
		}
	case 20: // MV-HEVC single-track, no EL
		if r.ELPresentFlag {
			issues = append(issues, "profile 20 (MV-HEVC) must not have ELPresentFlag set")
		}
	}

	// Dual-layer profiles (4, 7) require both BL and EL present
	if r.DVProfile == 4 || r.DVProfile == 7 {
		if !r.BLPresentFlag {
			issues = append(issues, fmt.Sprintf("profile %d (dual-layer) should have BLPresentFlag set", r.DVProfile))
		}
		if !r.ELPresentFlag {
			issues = append(issues, fmt.Sprintf("profile %d (dual-layer) should have ELPresentFlag set", r.DVProfile))
		}
	}

	qc := map[string]any{"ok": len(issues) == 0}
	if len(issues) > 0 {
		qc["issues"] = issues
	}
	if len(notes) > 0 {
		qc["notes"] = notes
	}
	return qc
}

// ── avcC ──────────────────────────────────────────────────────────────────────

func printAvcCInfo(box *mp4.AvcCBox) map[string]any {
	r := box.DecConfRec
	return map[string]any{
		"Profile":       fmt.Sprintf("0x%02x", r.AVCProfileIndication),
		"ProfileCompat": fmt.Sprintf("0x%02x", r.ProfileCompatibility),
		"Level":         fmt.Sprintf("0x%02x", r.AVCLevelIndication),
		"LengthSize":    4, // this fork enforces 4-byte NAL length size
		"NumSPS":        len(r.SPSnalus),
		"NumPPS":        len(r.PPSnalus),
	}
}

// ── hvcC ──────────────────────────────────────────────────────────────────────

func printHvcCInfo(hdcr hevc.DecConfRec) map[string]any {
	info := make(map[string]any)
	info["Profile"] = fmt.Sprintf("space=%d tier=%t idc=%d level=%d",
		hdcr.GeneralProfileSpace, hdcr.GeneralTierFlag,
		hdcr.GeneralProfileIDC, hdcr.GeneralLevelIDC)
	info["Chroma"] = hdcr.ChromaFormatIDC
	info["BitDepth"] = fmt.Sprintf("luma=%d chroma=%d",
		hdcr.BitDepthLumaMinus8+8,
		hdcr.BitDepthChromaMinus8+8)
	info["NumTemporalLayers"] = hdcr.NumTemporalLayers
	info["LengthSize"] = hdcr.LengthSizeMinusOne + 1

	vpsNalus := hdcr.GetNalusForType(hevc.NALU_VPS)
	if len(vpsNalus) > 0 {
		vps, err := hevc.ParseVPSNALUnit(vpsNalus[0])
		if err != nil {
			info["VPS"] = fmt.Sprintf("parse error: %v", err)
		} else {
			info["VPS"] = fmt.Sprintf("layers=%d,views=%d,multiLayer=%t",
				vps.GetNumLayers(), vps.GetNumViews(), vps.IsMultiLayer())
		}
	}

	for _, array := range hdcr.NaluArrays {
		info[fmt.Sprintf("%s", array.NaluType())] = fmt.Sprintf("%d nalus (complete=%d)", len(array.Nalus), array.Complete())
	}
	return info
}

// ── lhvC ──────────────────────────────────────────────────────────────────────

func printLhvCInfo(hdcr hevc.DecConfRec) map[string]any {
	info := make(map[string]any)
	info["NumTemporalLayers"] = hdcr.NumTemporalLayers
	info["LengthSize"] = hdcr.LengthSizeMinusOne + 1

	for _, array := range hdcr.NaluArrays {
		arrayKey := fmt.Sprintf("%s", array.NaluType())
		arrayInfo := map[string]any{
			"nalus":    len(array.Nalus),
			"complete": array.Complete(),
		}
		var naluData []string
		for _, nalu := range array.Nalus {
			naluData = append(naluData, hex.EncodeToString(nalu))
		}
		if len(naluData) > 0 {
			arrayInfo["data"] = naluData
		}
		info[arrayKey] = arrayInfo
	}
	return info
}

// ── colr ──────────────────────────────────────────────────────────────────────

func formatColrInfo(box *mp4.ColrBox) map[string]any {
	info := map[string]any{
		"ColorType": box.ColorType,
	}
	switch box.ColorType {
	case mp4.ColorTypeOnScreenColors, mp4.QuickTimeColorParameters:
		info["ColorPrimaries"] = fmt.Sprintf("%d (%s)", box.ColorPrimaries, nclxPrimariesName(box.ColorPrimaries))
		info["TransferCharacteristics"] = fmt.Sprintf("%d (%s)", box.TransferCharacteristics, nclxTransferName(box.TransferCharacteristics))
		info["MatrixCoefficients"] = fmt.Sprintf("%d (%s)", box.MatrixCoefficients, nclxMatrixName(box.MatrixCoefficients))
		if box.ColorType == mp4.ColorTypeOnScreenColors {
			info["FullRangeFlag"] = box.FullRangeFlag
		}
	}
	return info
}

func validateColrForDV(box *mp4.ColrBox) map[string]any {
	var issues, notes []string

	if box.ColorType != mp4.ColorTypeOnScreenColors {
		notes = append(notes, fmt.Sprintf("colorType is %q (not nclx) — cannot verify HDR colour metadata", box.ColorType))
	} else {
		if box.ColorPrimaries != 9 {
			issues = append(issues, fmt.Sprintf("expected BT.2020 color primaries (9), got %d", box.ColorPrimaries))
		}
		if box.TransferCharacteristics != 16 && box.TransferCharacteristics != 18 {
			issues = append(issues, fmt.Sprintf("expected PQ transfer (16) or HLG (18), got %d", box.TransferCharacteristics))
		}
		if box.MatrixCoefficients != 9 {
			notes = append(notes, fmt.Sprintf("expected BT.2020 nc matrix (9), got %d", box.MatrixCoefficients))
		}
		if box.FullRangeFlag {
			notes = append(notes, "FullRangeFlag=true — most DV content uses limited range (studio swing)")
		}
	}

	qc := map[string]any{"ok": len(issues) == 0}
	if len(issues) > 0 {
		qc["issues"] = issues
	}
	if len(notes) > 0 {
		qc["notes"] = notes
	}
	return qc
}

// ── mdcv ──────────────────────────────────────────────────────────────────────

func formatMdcvInfo(box *mp4.MdcvBox) map[string]any {
	// Primaries in CIE 1931 chromaticity coords * 50000; luminance in 0.0001 cd/m²
	return map[string]any{
		"DisplayPrimaries": fmt.Sprintf(
			"R(%.5f,%.5f) G(%.5f,%.5f) B(%.5f,%.5f)",
			float64(box.DisplayPrimariesX[0])/50000, float64(box.DisplayPrimariesY[0])/50000,
			float64(box.DisplayPrimariesX[1])/50000, float64(box.DisplayPrimariesY[1])/50000,
			float64(box.DisplayPrimariesX[2])/50000, float64(box.DisplayPrimariesY[2])/50000,
		),
		"WhitePoint": fmt.Sprintf("(%.5f,%.5f)",
			float64(box.WhitePointX)/50000, float64(box.WhitePointY)/50000),
		"MaxDisplayMasteringLuminance": fmt.Sprintf("%d (%.4f cd/m²)",
			box.MaxDisplayMasteringLuminance,
			float64(box.MaxDisplayMasteringLuminance)/10000),
		"MinDisplayMasteringLuminance": fmt.Sprintf("%d (%.4f cd/m²)",
			box.MinDisplayMasteringLuminance,
			float64(box.MinDisplayMasteringLuminance)/10000),
	}
}

// ── clli ──────────────────────────────────────────────────────────────────────

func formatClliInfo(box *mp4.ClliBox) map[string]any {
	return map[string]any{
		"MaxCLL":  fmt.Sprintf("%d cd/m²", box.MaxContentLightLevel),
		"MaxFALL": fmt.Sprintf("%d cd/m²", box.MaxPicAverageLightLevel),
	}
}

// ── vexu ──────────────────────────────────────────────────────────────────────

func printVexuInfo(vexu *mp4.VexuBox) map[string]any {
	info := map[string]any{}
	if vexu.Eyes != nil {
		eyes := vexu.Eyes
		if eyes.Stri != nil {
			info["stri"] = fmt.Sprintf("left=%t right=%t reversed=%t",
				eyes.Stri.HasLeftEye(),
				eyes.Stri.HasRightEye(),
				eyes.Stri.EyeViewsReversed())
		}
		if eyes.Hero != nil {
			info["hero"] = fmt.Sprintf("%s (%d)",
				eyes.Hero.HeroEyeName(), eyes.Hero.HeroEye)
		}
		if eyes.Cams != nil && eyes.Cams.Blin != nil {
			info["baseline"] = fmt.Sprintf("%d um (%.1f mm)",
				eyes.Cams.Blin.Baseline,
				float64(eyes.Cams.Blin.Baseline)/1000.0)
		}
	}
	if vexu.Proj != nil && vexu.Proj.Prji != nil {
		info["projection"] = fmt.Sprintf("%s", vexu.Proj.Prji.ProjectionType)
	}
	return info
}

// ── sample info ───────────────────────────────────────────────────────────────

func printUnfragmentedSampleInfo(trak *mp4.TrakBox, stbl *mp4.StblBox, timeScale uint32, opts InfoOptions) map[string]any {
	nrSamples := trak.GetNrSamples()
	var sampleDur uint32
	if stbl.Stts != nil && len(stbl.Stts.SampleTimeDelta) > 0 {
		sampleDur = stbl.Stts.SampleTimeDelta[0]
	}

	info := map[string]any{
		"Samples":   fmt.Sprintf("%d", nrSamples),
		"Timescale": fmt.Sprintf("%d", timeScale),
	}
	if sampleDur > 0 {
		fps := float64(timeScale) / float64(sampleDur)
		info["SampleDuration"] = fmt.Sprintf("%d (%.3f fps)", sampleDur, fps)
	}
	if stbl.Stss != nil && opts.ShowIDR {
		info[fmt.Sprintf("Sync (IDR) frames (%d)", len(stbl.Stss.SampleNumber))] = printSyncFrameInfo(stbl.Stss.SampleNumber)
	}
	return info
}

func printFragmentedInfo(f *mp4.File, trackID uint32, timeScale uint32, opts InfoOptions) map[string]any {
	var totalSamples uint32
	var sampleDur uint32
	var syncFrames []uint32
	sampleNr := uint32(0)

	for _, seg := range f.Segments {
		for _, frag := range seg.Fragments {
			if frag.Moof == nil {
				continue
			}
			for _, traf := range frag.Moof.Trafs {
				if traf.Tfhd.TrackID != trackID {
					continue
				}
				var defaultFlags uint32
				if traf.Tfhd.HasDefaultSampleFlags() {
					defaultFlags = traf.Tfhd.DefaultSampleFlags
				}
				if sampleDur == 0 && traf.Tfhd.HasDefaultSampleDuration() {
					sampleDur = traf.Tfhd.DefaultSampleDuration
				}
				for _, trun := range traf.Truns {
					for i, s := range trun.Samples {
						sampleNr++
						if sampleDur == 0 && s.Dur > 0 {
							sampleDur = s.Dur
						}
						flags := s.Flags
						if !trun.HasSampleFlags() {
							if i == 0 && trun.HasFirstSampleFlags() {
								fsf, _ := trun.FirstSampleFlags()
								flags = fsf
							} else {
								flags = defaultFlags
							}
						}
						if mp4.IsSyncSampleFlags(flags) {
							syncFrames = append(syncFrames, sampleNr)
						}
					}
					totalSamples += trun.SampleCount()
				}
			}
		}
	}

	info := map[string]any{
		"Samples":   fmt.Sprintf("%d", totalSamples),
		"Timescale": fmt.Sprintf("%d", timeScale),
	}
	if sampleDur > 0 {
		fps := float64(timeScale) / float64(sampleDur)
		info["SampleDuration"] = fmt.Sprintf("%d (%.3f fps)", sampleDur, fps)
	}
	if opts.ShowIDR && len(syncFrames) > 0 {
		info[fmt.Sprintf("Sync (IDR) frames (%d)", len(syncFrames))] = printSyncFrameInfo(syncFrames)
	}
	return info
}

func printSyncFrameInfo(syncNrs []uint32) []string {
	var frames []string
	for i, sn := range syncNrs {
		if i == 0 {
			frames = append(frames, fmt.Sprintf("frame %d", sn))
		} else {
			interval := sn - syncNrs[i-1]
			frames = append(frames, fmt.Sprintf("frame %d (interval=%d)", sn, interval))
		}
	}
	return frames
}

// ── trgr ──────────────────────────────────────────────────────────────────────

func printTrackGroupInfo(trak *mp4.TrakBox) map[string]any {
	info := make(map[string]any)
	for _, child := range trak.Trgr.Children {
		if cstg, ok := child.(*mp4.TrackGroupTypeBox); ok {
			info[cstg.Type()] = fmt.Sprintf("trackGroupID=%d", cstg.TrackGroupID)
		}
	}
	return info
}

// ── file-level QC ─────────────────────────────────────────────────────────────

func fileLevelDVQC(f *mp4.File, dvTrackIDs []uint32) map[string]any {
	var issues, notes []string

	if len(dvTrackIDs) == 0 {
		issues = append(issues, "no Dolby Vision tracks found (no dvcC or dvvC box in any visual sample entry)")
	} else {
		notes = append(notes, fmt.Sprintf("DV track IDs: %v", dvTrackIDs))
	}

	if len(dvTrackIDs) > 1 {
		notes = append(notes, fmt.Sprintf(
			"%d DV tracks found — expected 1 for single-track profiles; verify dual-layer packaging is intentional",
			len(dvTrackIDs)))
	}

	if f.IsFragmented() {
		notes = append(notes, "fragmented MP4 (fMP4) — suitable for DASH/HLS delivery")
	} else if f.Moov != nil {
		notes = append(notes, "progressive MP4 — moov present")
	}

	qc := map[string]any{"ok": len(issues) == 0}
	if len(issues) > 0 {
		qc["issues"] = issues
	}
	if len(notes) > 0 {
		qc["notes"] = notes
	}
	return qc
}

// ── lookup tables ─────────────────────────────────────────────────────────────

func dvProfileName(p uint8) string {
	names := map[uint8]string{
		0:  "dvhe.dtr",
		1:  "dvhe.dth",
		2:  "dvhe.dtb",
		3:  "dvhe.st",
		4:  "dvhe.04 (BL+EL HDR10)",
		5:  "dvhe.05 (SL IPTPQc2)",
		6:  "dvhe.06 (BL+EL IPTPQc2)",
		7:  "dvhe.07 (BL+EL+RPU 4K)",
		8:  "dvhe.08 (BL HDR10/SDR compat)",
		9:  "dvav.09 (AVC BL)",
		20: "dvhe.20 (MV-HEVC)",
	}
	if n, ok := names[p]; ok {
		return n
	}
	return "unknown"
}

func dvLevelDesc(l uint8) string {
	descs := map[uint8]string{
		1:  "HD 24fps",
		2:  "HD 30fps",
		3:  "HD 60fps",
		4:  "HD 120fps",
		5:  "UHD 24fps",
		6:  "UHD 30fps",
		7:  "UHD 48fps",
		8:  "UHD 60fps",
		9:  "UHD 120fps",
		10: "UHD+ 60fps",
		11: "UHD+ 120fps",
		12: "8K 60fps",
		13: "8K 120fps",
	}
	if d, ok := descs[l]; ok {
		return d
	}
	return "unknown"
}

func dvCompatName(id uint8) string {
	names := map[uint8]string{
		0: "none",
		1: "HDR10",
		2: "SDR",
		3: "HLG",
		4: "HDR10+",
	}
	if n, ok := names[id]; ok {
		return n
	}
	return "unknown"
}

func nclxPrimariesName(p uint16) string {
	names := map[uint16]string{
		1:  "BT.709",
		9:  "BT.2020",
		12: "DCI-P3",
	}
	if n, ok := names[p]; ok {
		return n
	}
	return "other"
}

func nclxTransferName(t uint16) string {
	names := map[uint16]string{
		1:  "BT.709",
		16: "PQ (ST 2084)",
		18: "HLG",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return "other"
}

func nclxMatrixName(m uint16) string {
	names := map[uint16]string{
		1:  "BT.709",
		9:  "BT.2020 nc",
		10: "BT.2020 c",
	}
	if n, ok := names[m]; ok {
		return n
	}
	return "other"
}
