package sdr

import (
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

// Info inspects an MP4 file for SDR correctness and QC.
func Info(inputPath string, opts InfoOptions) (map[string]any, error) {
	ifd, err := os.Open(inputPath)
	if err != nil {
		return nil, fmt.Errorf("could not open input file: %w", err)
	}
	defer func() { _ = ifd.Close() }()

	// Non-fatal decode errors: some files have unknown/proprietary top-level
	// boxes after mdat that mp4ff cannot parse. As long as moov was decoded we
	// have everything we need.
	parsedMP4, decodeErr := mp4.DecodeFile(ifd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if parsedMP4 == nil {
		return nil, fmt.Errorf("could not decode MP4: %w", decodeErr)
	}

	if parsedMP4.Moov == nil {
		return nil, fmt.Errorf("no moov box found")
	}

	results := make(map[string]any)

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

		for _, child := range stbl.Stsd.Children {
			vse, ok := child.(*mp4.VisualSampleEntryBox)
			if !ok {
				continue
			}

			track["Sample entry"] = fmt.Sprintf("%s (%dx%d)", vse.Type(), vse.Width, vse.Height)

			// Codec configs
			if vse.HvcC != nil {
				track["hvcC (HEVC config)"] = printHvcCInfo(vse.HvcC.DecConfRec)
			}
			if vse.AvcC != nil {
				track["avcC (AVC config)"] = printAvcCInfo(vse.AvcC)
			}

			// Color metadata
			for _, vseChild := range vse.Children {
				if colr, ok := vseChild.(*mp4.ColrBox); ok {
					track["colr (Color info)"] = formatColrInfo(colr)
					track["colr QC"] = validateColrForSDR(colr)
					break
				}
			}

			// HDR metadata boxes — should not be present in SDR
			if vse.Mdcv != nil {
				track["mdcv (Mastering display) QC"] = map[string]any{
					"ok":     false,
					"issues": []string{"mdcv box present — mastering display metadata is unexpected in SDR content"},
				}
			}
			if vse.Clli != nil {
				track["clli (Content light level) QC"] = map[string]any{
					"ok":     false,
					"issues": []string{"clli box present — content light level metadata is unexpected in SDR content"},
				}
			}

			// DV signaling — should not be present in SDR
			for _, vseChild := range vse.Children {
				ub, ok := vseChild.(*mp4.UnknownBox)
				if !ok {
					continue
				}
				if ub.Type() == "dvcC" || ub.Type() == "dvvC" {
					track[fmt.Sprintf("%s QC", ub.Type())] = map[string]any{
						"ok":     false,
						"issues": []string{fmt.Sprintf("%s box present — Dolby Vision signaling is unexpected in SDR content", ub.Type())},
					}
				}
			}
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

	results["File-level QC"] = fileLevelSDRQC(parsedMP4)
	if decodeErr != nil {
		results["parse_warning"] = decodeErr.Error()
	}
	return results, nil
}

// ── colr ──────────────────────────────────────────────────────────────────────

func formatColrInfo(box *mp4.ColrBox) map[string]any {
	info := map[string]any{
		"ColorType": box.ColorType,
	}
	switch box.ColorType {
	case mp4.ColorTypeOnScreenColors, mp4.QuickTimeColorParameters:
		info["ColorPrimaries"] = fmt.Sprintf("%d (%s)", box.ColorPrimaries, sdrPrimariesName(box.ColorPrimaries))
		info["TransferCharacteristics"] = fmt.Sprintf("%d (%s)", box.TransferCharacteristics, sdrTransferName(box.TransferCharacteristics))
		info["MatrixCoefficients"] = fmt.Sprintf("%d (%s)", box.MatrixCoefficients, sdrMatrixName(box.MatrixCoefficients))
		if box.ColorType == mp4.ColorTypeOnScreenColors {
			info["FullRangeFlag"] = box.FullRangeFlag
		}
	}
	return info
}

func validateColrForSDR(box *mp4.ColrBox) map[string]any {
	var issues, notes []string

	if box.ColorType != mp4.ColorTypeOnScreenColors {
		notes = append(notes, fmt.Sprintf("colorType is %q (not nclx) — cannot verify SDR colour metadata", box.ColorType))
	} else {
		// HDR transfer functions are a hard failure for SDR
		if box.TransferCharacteristics == 16 {
			issues = append(issues, "TransferCharacteristics=16 (PQ/ST 2084) — this is HDR10, not SDR")
		} else if box.TransferCharacteristics == 18 {
			issues = append(issues, "TransferCharacteristics=18 (HLG) — this is HDR, not SDR")
		} else if box.TransferCharacteristics != 1 && box.TransferCharacteristics != 6 &&
			box.TransferCharacteristics != 13 {
			notes = append(notes, fmt.Sprintf("TransferCharacteristics=%d (%s) — expected BT.709 (1 or 6) or sRGB (13) for SDR",
				box.TransferCharacteristics, sdrTransferName(box.TransferCharacteristics)))
		}

		// BT.2020 primaries with non-HDR transfer could be UHD SDR — note rather than fail
		if box.ColorPrimaries == 9 {
			notes = append(notes, "ColorPrimaries=9 (BT.2020) — valid for UHD SDR but verify transfer is not HDR")
		} else if box.ColorPrimaries != 1 && box.ColorPrimaries != 5 && box.ColorPrimaries != 6 {
			notes = append(notes, fmt.Sprintf("ColorPrimaries=%d (%s) — expected BT.709 (1) for HD SDR",
				box.ColorPrimaries, sdrPrimariesName(box.ColorPrimaries)))
		}

		if box.FullRangeFlag {
			notes = append(notes, "FullRangeFlag=true — broadcast SDR typically uses limited range (studio swing)")
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

func fileLevelSDRQC(f *mp4.File) map[string]any {
	var issues, notes []string

	if f.IsFragmented() {
		notes = append(notes, "fragmented MP4 (fMP4) — suitable for DASH/HLS delivery")
	} else if f.Moov != nil {
		notes = append(notes, "progressive MP4 — moov present")
	}

	// Check all visual tracks for unexpected HDR/DV signals at file level
	if f.Moov != nil {
		for _, trak := range f.Moov.Traks {
			if trak.Mdia == nil || trak.Mdia.Minf == nil ||
				trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil {
				continue
			}
			for _, child := range trak.Mdia.Minf.Stbl.Stsd.Children {
				vse, ok := child.(*mp4.VisualSampleEntryBox)
				if !ok {
					continue
				}
				if vse.Mdcv != nil || vse.Clli != nil {
					issues = append(issues, fmt.Sprintf("track %d: HDR metadata boxes (mdcv/clli) found — not expected for SDR", trak.Tkhd.TrackID))
				}
				for _, vseChild := range vse.Children {
					if ub, ok := vseChild.(*mp4.UnknownBox); ok {
						if ub.Type() == "dvcC" || ub.Type() == "dvvC" {
							issues = append(issues, fmt.Sprintf("track %d: Dolby Vision box (%s) found — not expected for SDR", trak.Tkhd.TrackID, ub.Type()))
						}
					}
				}
			}
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

// ── lookup tables ─────────────────────────────────────────────────────────────

func sdrPrimariesName(p uint16) string {
	names := map[uint16]string{
		1: "BT.709",
		4: "BT.470M",
		5: "BT.470BG / PAL",
		6: "BT.601 / SMPTE 170M",
		7: "SMPTE 240M",
		9: "BT.2020",
	}
	if n, ok := names[p]; ok {
		return n
	}
	return "other"
}

func sdrTransferName(t uint16) string {
	names := map[uint16]string{
		1:  "BT.709",
		4:  "Gamma 2.2",
		5:  "Gamma 2.8",
		6:  "BT.601 / BT.709",
		7:  "SMPTE 240M",
		8:  "Linear",
		13: "sRGB / IEC 61966-2-1",
		14: "BT.2020 10-bit",
		15: "BT.2020 12-bit",
		16: "PQ / ST 2084 (HDR10)",
		18: "HLG (HDR)",
	}
	if n, ok := names[t]; ok {
		return n
	}
	return "other"
}

func sdrMatrixName(m uint16) string {
	names := map[uint16]string{
		1: "BT.709",
		5: "BT.470BG",
		6: "BT.601 / SMPTE 170M",
		7: "SMPTE 240M",
		9: "BT.2020 nc",
	}
	if n, ok := names[m]; ok {
		return n
	}
	return "other"
}
