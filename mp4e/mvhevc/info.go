package mvhevc

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

type InfoOptions struct {
	ShowIDR bool
}

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
			for k, v := range printVisualSampleEntryInfo(vse) {
				track[k] = v
			}
		}

		timeScale := trak.Mdia.Mdhd.Timescale
		trackID := trak.Tkhd.TrackID

		if parsedMP4.IsFragmented() {
			track["Samples Info"] = printFragmentedInfo(parsedMP4, trackID, timeScale, opts)
		} else {
			track["Samples Info"] = printUnfragmentedSampleInfo(trak, stbl, timeScale, opts)
		}

		var oinfs []map[string]any
		var linfs []map[string]any
		for _, child := range stbl.Children {
			sgpd, ok := child.(*mp4.SgpdBox)
			if !ok {
				continue
			}
			switch sgpd.GroupingType {
			case "oinf":
				oinfs = printOinfInfo(sgpd)
			case "linf":
				linfs = printLinfInfo(sgpd)
			}
		}

		if len(oinfs) > 0 {
			track["oinf (Operating Points Information)"] = oinfs
		}

		if len(linfs) > 0 {
			track["linf (Layer Information)"] = linfs
		}

		var trackGroups map[string]any
		if trak.Trgr != nil {
			trackGroups = printTrackGroupInfo(trak)
			track["trgr (Track Group)"] = trackGroups
		}
		results[key] = track
	}

	return results, nil
}

func printVisualSampleEntryInfo(vse *mp4.VisualSampleEntryBox) map[string]any {
	info := map[string]any{
		"Sample entry": fmt.Sprintf("%s (%dx%d)", vse.Type(), vse.Width, vse.Height),
	}
	if vse.HvcC != nil {
		info["hvcC (base layer config)"] = printHvcCInfo(vse.HvcC.DecConfRec)
	}
	if vse.LhvC != nil {
		info["lhvC (enhancement layer config)"] = printLhvCInfo(vse.LhvC.DecConfRec)
	}
	if vse.Vexu != nil {
		info["vexu (Spatial Video)"] = printVexuInfo(vse.Vexu)
	}
	if vse.Hfov != nil {
		info["hfov"] = fmt.Sprintf("%d/1000 degrees (%.1f)",
			vse.Hfov.FieldOfView,
			float64(vse.Hfov.FieldOfView)/1000.0)
	}
	for _, child := range vse.Children {
		if colr, ok := child.(*mp4.ColrBox); ok {
			info["colr"] = printColrInfo(colr)
		}
	}
	return info
}

func printHvcCInfo(hdcr hevc.DecConfRec) map[string]any {
	hvCCInfo := make(map[string]any)
	hvCCInfo["Profile"] = fmt.Sprintf("space=%d tier=%t idc=%d level=%d",
		hdcr.GeneralProfileSpace, hdcr.GeneralTierFlag,
		hdcr.GeneralProfileIDC, hdcr.GeneralLevelIDC)
	hvCCInfo["Chroma"] = hdcr.ChromaFormatIDC
	hvCCInfo["BitDepth"] = fmt.Sprintf("luma=%d chroma=%d",
		hdcr.BitDepthLumaMinus8+8,
		hdcr.BitDepthChromaMinus8+8)
	hvCCInfo["NumTemporalLayers"] = hdcr.NumTemporalLayers
	hvCCInfo["LengthSize"] = hdcr.LengthSizeMinusOne + 1

	vpsNalus := hdcr.GetNalusForType(hevc.NALU_VPS)
	if len(vpsNalus) > 0 {
		vps, err := hevc.ParseVPSNALUnit(vpsNalus[0])
		if err != nil {
			hvCCInfo["VPS"] = fmt.Sprintf("parse error: %v", err.Error())
		} else {
			hvCCInfo["VPS"] = fmt.Sprintf("layers=%d,views=%d,multiLayer=%t",
				vps.GetNumLayers(), vps.GetNumViews(), vps.IsMultiLayer())
		}
	}

	for _, array := range hdcr.NaluArrays {
		hvCCInfo[fmt.Sprintf("%s", array.NaluType())] = fmt.Sprintf("%d nalus (complete=%d)", len(array.Nalus), array.Complete())
	}
	return hvCCInfo
}

func printLhvCInfo(hdcr hevc.DecConfRec) map[string]any {
	lhvcInfo := make(map[string]any)
	lhvcInfo["NumTemporalLayers"] = hdcr.NumTemporalLayers
	lhvcInfo["LengthSize"] = hdcr.LengthSizeMinusOne + 1

	for _, array := range hdcr.NaluArrays {
		arrayKey := fmt.Sprintf("%s", array.NaluType())

		arrayInfo := map[string]any{
			"nalus":    len(array.Nalus),
			"complete": array.Complete(),
		}

		var naluData []string
		for _, nalu := range array.Nalus {
			hexStr := hex.EncodeToString(nalu)
			naluData = append(naluData, hexStr)
		}
		if len(naluData) > 0 {
			arrayInfo["data"] = naluData
		}
		lhvcInfo[arrayKey] = arrayInfo
	}
	return lhvcInfo
}

func printColrInfo(colr *mp4.ColrBox) map[string]any {
	info := map[string]any{
		"type": fmt.Sprintf("%s", colr.ColorType),
	}
	switch colr.ColorType {
	case mp4.ColorTypeOnScreenColors:
		info["primaries"] = colr.ColorPrimaries
		info["transfer"] = colr.TransferCharacteristics
		info["matrix"] = colr.MatrixCoefficients
		info["full_range"] = colr.FullRangeFlag
	case mp4.QuickTimeColorParameters:
		info["primaries"] = colr.ColorPrimaries
		info["transfer"] = colr.TransferCharacteristics
		info["matrix"] = colr.MatrixCoefficients
	}
	return info
}

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

func printUnfragmentedSampleInfo(trak *mp4.TrakBox, stbl *mp4.StblBox, timeScale uint32, opts InfoOptions) map[string]any {
	nrSamples := trak.GetNrSamples()
	var sampleDur uint32
	if stbl.Stts != nil && len(stbl.Stts.SampleTimeDelta) > 0 {
		sampleDur = stbl.Stts.SampleTimeDelta[0]
	}

	info := map[string]any{}
	if sampleDur > 0 {
		fps := float64(timeScale) / float64(sampleDur)

		info["Samples"] = fmt.Sprintf("%d", nrSamples)
		info["Timescale"] = fmt.Sprintf("%d", timeScale)
		info["SampleDuration"] = fmt.Sprintf("%d (%.3f fps)", sampleDur, fps)
	} else {
		info["Samples"] = fmt.Sprintf("%d", nrSamples)
		info["Timescale"] = fmt.Sprintf("%d", timeScale)
	}

	if stbl.Stss != nil && opts.ShowIDR {
		info[fmt.Sprintf("Sync (IDR) frames (%d)", len(stbl.Stss.SampleNumber))] = printSyncFrameInfo(stbl.Stss.SampleNumber)
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

func printOinfInfo(sgpd *mp4.SgpdBox) []map[string]any {
	var infoArr []map[string]any
	for _, entry := range sgpd.SampleGroupEntries {
		oinf, ok := entry.(*mp4.OinfSampleGroupEntry)
		if !ok {
			continue
		}

		info := map[string]any{
			"ScalabilityMask": fmt.Sprintf("0x%04x", oinf.ScalabilityMask),
		}

		profiles := map[string]any{
			"total": fmt.Sprintf("%d", len(oinf.ProfileTierLevels)),
		}
		levels := make(map[string]any)
		for j, ptl := range oinf.ProfileTierLevels {
			levels[fmt.Sprintf("PTL[%d]", j)] = fmt.Sprintf("space=%d tier=%t profile=%d level=%d",
				ptl.GeneralProfileSpace, ptl.GeneralTierFlag, ptl.GeneralProfileIDC, ptl.GeneralLevelIDC)
		}
		profiles["levels"] = levels
		info["ProfileTierLevels"] = profiles

		// ====

		opPoints := map[string]any{
			"total": fmt.Sprintf("%d", len(oinf.OperatingPoints)),
		}
		ops := make(map[string]any)
		for j, op := range oinf.OperatingPoints {

			opVal := fmt.Sprintf("olsIdx=%d maxTid=%d layers=%d dims=%dx%d-%dx%d",
				op.OutputLayerSetIdx, op.MaxTemporalID, len(op.Layers),
				op.MinPicWidth, op.MinPicHeight, op.MaxPicWidth, op.MaxPicHeight)

			layerInfo := make(map[string]any)
			for k, l := range op.Layers {
				layerInfo[fmt.Sprintf("layer[%d]", k)] = fmt.Sprintf("ptlIdx=%d layerId=%d output=%t",
					l.PtlIdx, l.LayerID, l.IsOutputLayer)
			}
			ops[fmt.Sprintf("OP[%d]", j)] = map[string]any{
				"info":   opVal,
				"layers": layerInfo,
			}
		}
		opPoints["points"] = ops
		info["OperatingPoints"] = opPoints

		// ===

		dependencyLayers := map[string]any{
			"total": fmt.Sprintf("%d", len(oinf.DependencyLayers)),
		}
		layers := make(map[string]any)
		for j, dep := range oinf.DependencyLayers {
			layers[fmt.Sprintf("Dep[%d]", j)] = fmt.Sprintf("layerId=%d dependsOn=%v dimIds=%v",
				dep.LayerID, dep.DependsOnLayers, dep.DimensionIds)
		}
		dependencyLayers["layers"] = layers
		info["DependencyLayers"] = dependencyLayers
		infoArr = append(infoArr, info)
	}
	return infoArr
}

func printLinfInfo(sgpd *mp4.SgpdBox) []map[string]any {
	var infoArr []map[string]any
	for _, entry := range sgpd.SampleGroupEntries {
		linf, ok := entry.(*mp4.LinfSampleGroupEntry)
		if !ok {
			continue
		}

		info := map[string]any{
			"total": len(linf.Layers),
		}

		layers := make(map[string]any)
		for j, l := range linf.Layers {
			layers[fmt.Sprintf("Layer[%d]", j)] = fmt.Sprintf("layerId=%d minTid=%d maxTid=%d subFlags=0x%02x",
				l.LayerID, l.MinTemporalID, l.MaxTemporalID, l.SubLayerPresenceFlags)
		}
		info["layers"] = layers
		infoArr = append(infoArr, info)
	}
	return infoArr
}

func printTrackGroupInfo(trak *mp4.TrakBox) map[string]any {
	info := make(map[string]any)
	for _, child := range trak.Trgr.Children {
		if cstg, ok := child.(*mp4.TrackGroupTypeBox); ok {
			info[cstg.Type()] = fmt.Sprintf("trackGroupID=%d", cstg.TrackGroupID)
		}
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

	info := map[string]any{}
	if sampleDur > 0 {
		fps := float64(timeScale) / float64(sampleDur)

		info["Samples"] = fmt.Sprintf("%d", totalSamples)
		info["Timescale"] = fmt.Sprintf("%d", timeScale)
		info["SampleDuration"] = fmt.Sprintf("%d (%.3f fps)", sampleDur, fps)

	} else {
		info["Samples"] = fmt.Sprintf("%d", totalSamples)
		info["Timescale"] = fmt.Sprintf("%d", timeScale)
	}

	if opts.ShowIDR && len(syncFrames) > 0 {
		info[fmt.Sprintf("Sync (IDR) frames (%d)", len(syncFrames))] = printSyncFrameInfo(syncFrames)
	}
	return info
}
