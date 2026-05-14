package mvhevc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

type InfoOptions struct {
	ShowIDR bool
	Json    bool
}

func Info(inputPath string, opts InfoOptions, w io.Writer) error {
	if w == nil {
		w = io.Discard
	}

	ifd, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("could not open input file: %w", err)
	}
	defer func() { _ = ifd.Close() }()

	parsedMP4, err := mp4.DecodeFile(ifd, mp4.WithDecodeMode(mp4.DecModeLazyMdat))
	if err != nil {
		return fmt.Errorf("could not decode MP4: %w", err)
	}

	if parsedMP4.Moov == nil {
		return fmt.Errorf("no moov box found")
	}

	tracks := make([]map[string]interface{}, 0)
	for i, trak := range parsedMP4.Moov.Traks {

		track := map[string]interface{}{
			"index":    i + 1,
			"track_id": trak.Tkhd.TrackID,
		}
		if !opts.Json {
			fmt.Fprintf(w, "Track %d (ID=%d):\n", track["index"], track["track_id"])
		}

		stbl := trak.Mdia.Minf.Stbl
		if stbl == nil || stbl.Stsd == nil {
			tracks = append(tracks, track)
			continue
		}

		for _, child := range stbl.Stsd.Children {
			vse, ok := child.(*mp4.VisualSampleEntryBox)
			if !ok {
				continue
			}

			sampleEntryInfo := printVisualSampleEntryInfo(vse, opts, w)
			track["sample_entry"] = sampleEntryInfo
		}

		timeScale := trak.Mdia.Mdhd.Timescale
		track["timescale"] = timeScale
		trackID := trak.Tkhd.TrackID

		if parsedMP4.IsFragmented() {
			track["fragmented_info"] = printFragmentedInfo(parsedMP4, trackID, timeScale, opts, w)
		} else {
			track["unfragmented_info"] = printUnfragmentedSampleInfo(trak, stbl, timeScale, opts, w)
		}

		for _, child := range stbl.Children {
			sgpd, ok := child.(*mp4.SgpdBox)
			if !ok {
				continue
			}
			switch sgpd.GroupingType {
			case "oinf":
				track["oinf"] = printOinfInfo(sgpd, opts, w)
			case "linf":
				track["linf"] = printLinfInfo(sgpd, opts, w)
			}
		}

		track["track_groups"] = printTrackGroupInfo(trak, opts, w)
		tracks = append(tracks, track)
	}

	if opts.Json {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(tracks)
	}

	return nil
}

func printVisualSampleEntryInfo(vse *mp4.VisualSampleEntryBox, opts InfoOptions, w io.Writer) map[string]interface{} {

	res := make(map[string]interface{})
	res["sample_entry"] = map[string]interface{}{
		"type":   vse.Type(),
		"width":  vse.Width,
		"height": vse.Height,
	}

	if !opts.Json {
		fmt.Fprintf(w, "  Sample entry: %s (%dx%d)\n", vse.Type(), vse.Width, vse.Height)
	}

	if vse.HvcC != nil {
		hvcc := printHvcCInfo(vse.HvcC.DecConfRec, opts, w)
		res["hvcc"] = hvcc
	}

	if vse.LhvC != nil {
		lhvc := printLhvCInfo(vse.LhvC.DecConfRec, opts, w)
		res["lhvc"] = lhvc
	}

	if vse.Vexu != nil {
		vexu := printVexuInfo(vse.Vexu, opts, w)
		res["vexu"] = vexu
	}

	if vse.Hfov != nil {
		res["hfov"] = float64(vse.Hfov.FieldOfView) / 1000.0
		if !opts.Json {
			fmt.Fprintf(w, "  hfov: %d/1000 degrees (%.1f)\n",
				vse.Hfov.FieldOfView,
				float64(vse.Hfov.FieldOfView)/1000.0)
		}
	}
	return res
}

func printHvcCInfo(hdcr hevc.DecConfRec, opts InfoOptions, w io.Writer) map[string]interface{} {

	hvcc := map[string]interface{}{
		"profile_space":       hdcr.GeneralProfileSpace,
		"tier_flag":           hdcr.GeneralTierFlag,
		"profile_idc":         hdcr.GeneralProfileIDC,
		"level_idc":           hdcr.GeneralLevelIDC,
		"chroma_format":       hdcr.ChromaFormatIDC,
		"bit_depth_luma":      hdcr.BitDepthLumaMinus8 + 8,
		"bit_depth_chroma":    hdcr.BitDepthChromaMinus8 + 8,
		"num_temporal_layers": hdcr.NumTemporalLayers,
		"length_size":         hdcr.LengthSizeMinusOne + 1,
	}

	if !opts.Json {
		fmt.Fprintf(w, "  hvcC (base layer config):\n")
		fmt.Fprintf(w, "    Profile: space=%d tier=%t idc=%d level=%d\n",
			hdcr.GeneralProfileSpace, hdcr.GeneralTierFlag,
			hdcr.GeneralProfileIDC, hdcr.GeneralLevelIDC)
		fmt.Fprintf(w, "    Chroma: %d  BitDepth: luma=%d chroma=%d\n",
			hdcr.ChromaFormatIDC,
			hdcr.BitDepthLumaMinus8+8,
			hdcr.BitDepthChromaMinus8+8)
		fmt.Fprintf(w, "    NumTemporalLayers: %d  LengthSize: %d\n",
			hdcr.NumTemporalLayers, hdcr.LengthSizeMinusOne+1)
	}

	vpsNalus := hdcr.GetNalusForType(hevc.NALU_VPS)
	if len(vpsNalus) > 0 {
		vps, err := hevc.ParseVPSNALUnit(vpsNalus[0])
		if err != nil {
			hvcc["vps_parse_error"] = err.Error()
			if !opts.Json {
				fmt.Fprintf(w, "    VPS parse error: %v\n", err)
			}
		} else {
			hvcc["num_layers"] = vps.GetNumLayers()
			hvcc["num_views"] = vps.GetNumViews()
			hvcc["multi_layer"] = vps.IsMultiLayer()
			if !opts.Json {
				fmt.Fprintf(w, "    VPS: layers=%d views=%d multiLayer=%t\n",
					vps.GetNumLayers(), vps.GetNumViews(), vps.IsMultiLayer())
			}
		}
	}

	arrays := make([]map[string]interface{}, 0)
	for _, array := range hdcr.NaluArrays {
		arrays = append(arrays, map[string]interface{}{
			"type":      array.NaluType(),
			"num_nalus": len(array.Nalus),
			"complete":  array.Complete(),
		})
		if !opts.Json {
			fmt.Fprintf(w, "    %s: %d nalus (complete=%d)\n",
				array.NaluType(), len(array.Nalus), array.Complete())
		}
	}
	hvcc["nalu_arrays"] = arrays
	return hvcc
}

func printLhvCInfo(hdcr hevc.DecConfRec, opts InfoOptions, w io.Writer) map[string]interface{} {
	lhvc := map[string]interface{}{
		"num_temporal_layers": hdcr.NumTemporalLayers,
		"length_size":         hdcr.LengthSizeMinusOne + 1,
	}

	var arrays []map[string]interface{}
	for _, array := range hdcr.NaluArrays {
		a := map[string]interface{}{
			"type":      array.NaluType(),
			"num_nalus": len(array.Nalus),
			"complete":  array.Complete(),
		}

		var nalus []string
		for _, nalu := range array.Nalus {
			nalus = append(nalus, hex.EncodeToString(nalu))
		}

		if len(nalus) > 0 {
			a["nalus"] = nalus
		}
		arrays = append(arrays, a)
	}

	lhvc["nalu_arrays"] = arrays

	if !opts.Json {
		fmt.Fprintf(w, "  lhvC (enhancement layer config):\n")
		fmt.Fprintf(w, "    NumTemporalLayers: %d  LengthSize: %d\n",
			hdcr.NumTemporalLayers, hdcr.LengthSizeMinusOne+1)
		for _, array := range hdcr.NaluArrays {
			fmt.Fprintf(w, "    %s: %d nalus (complete=%d)\n",
				array.NaluType(), len(array.Nalus), array.Complete())
			for _, nalu := range array.Nalus {
				fmt.Fprintf(w, "      %s\n", hex.EncodeToString(nalu))
			}
		}
	}
	return lhvc
}

func printVexuInfo(vexu *mp4.VexuBox, opts InfoOptions, w io.Writer) map[string]interface{} {

	res := make(map[string]interface{})

	if !opts.Json {
		fmt.Fprintf(w, "  vexu (Spatial Video):\n")
	}

	if vexu.Eyes != nil {
		eyes := vexu.Eyes
		if eyes.Stri != nil {
			res["stri"] = map[string]interface{}{
				"left":     eyes.Stri.HasLeftEye(),
				"right":    eyes.Stri.HasRightEye(),
				"reversed": eyes.Stri.EyeViewsReversed(),
			}
			if !opts.Json {
				fmt.Fprintf(w, "    stri: left=%t right=%t reversed=%t\n",
					eyes.Stri.HasLeftEye(),
					eyes.Stri.HasRightEye(),
					eyes.Stri.EyeViewsReversed())
			}
		}
		if eyes.Hero != nil {
			res["hero"] = map[string]interface{}{
				"eye": eyes.Hero.HeroEyeName(),
				"id":  eyes.Hero.HeroEye,
			}
			if !opts.Json {
				fmt.Fprintf(w, "    hero: %s (%d)\n",
					eyes.Hero.HeroEyeName(), eyes.Hero.HeroEye)
			}
		}
		if eyes.Cams != nil && eyes.Cams.Blin != nil {
			res["baseline_mm"] = float64(eyes.Cams.Blin.Baseline) / 1000.0
			res["baseline_um"] = eyes.Cams.Blin.Baseline
			if !opts.Json {
				fmt.Fprintf(w, "    baseline: %d um (%.1f mm)\n",
					eyes.Cams.Blin.Baseline,
					float64(eyes.Cams.Blin.Baseline)/1000.0)
			}
		}
	}
	if vexu.Proj != nil && vexu.Proj.Prji != nil {
		res["projection"] = vexu.Proj.Prji.ProjectionType
		if !opts.Json {
			fmt.Fprintf(w, "    projection: %s\n", vexu.Proj.Prji.ProjectionType)
		}
	}
	return res
}

func printUnfragmentedSampleInfo(trak *mp4.TrakBox, stbl *mp4.StblBox, timeScale uint32, opts InfoOptions, w io.Writer) map[string]interface{} {
	nrSamples := trak.GetNrSamples()

	info := map[string]interface{}{
		"samples":   nrSamples,
		"timescale": timeScale,
	}

	var sampleDur uint32
	if stbl.Stts != nil && len(stbl.Stts.SampleTimeDelta) > 0 {
		sampleDur = stbl.Stts.SampleTimeDelta[0]
	}
	if sampleDur > 0 {
		fps := float64(timeScale) / float64(sampleDur)

		info["sample_duration"] = sampleDur
		info["fps"] = fps

		if !opts.Json {
			fmt.Fprintf(w, "  Samples: %d, Timescale: %d, SampleDur: %d (%.3f fps)\n",
				nrSamples, timeScale, sampleDur, fps)
		}

	} else if !opts.Json {
		fmt.Fprintf(w, "  Samples: %d, Timescale: %d\n", nrSamples, timeScale)
	}
	if stbl.Stss != nil && opts.ShowIDR {
		info["sync_frames"] = printSyncFrameInfo(stbl.Stss.SampleNumber, opts, w)
	}
	return info
}

func printSyncFrameInfo(syncNrs []uint32, opts InfoOptions, w io.Writer) map[string]interface{} {
	frames := make([]map[string]interface{}, 0)

	if !opts.Json {
		fmt.Fprintf(w, "  Sync (IDR) frames (%d):\n", len(syncNrs))
	}

	for i, sn := range syncNrs {
		frame := map[string]interface{}{
			"frame": sn,
		}

		if i == 0 {
			if !opts.Json {
				fmt.Fprintf(w, "    frame %d\n", sn)
			}
		} else {
			interval := sn - syncNrs[i-1]
			frame["interval"] = interval
			if !opts.Json {
				fmt.Fprintf(w, "    frame %d (interval=%d)\n", sn, interval)
			}
		}
		frames = append(frames, frame)
	}
	return map[string]interface{}{
		"count":  len(syncNrs),
		"frames": frames,
	}
}

func printOinfInfo(sgpd *mp4.SgpdBox, opts InfoOptions, w io.Writer) []map[string]interface{} {

	oinfEntries := make([]map[string]interface{}, 0)

	for _, entry := range sgpd.SampleGroupEntries {
		oinf, ok := entry.(*mp4.OinfSampleGroupEntry)
		if !ok {
			continue
		}

		entryInfo := map[string]interface{}{
			"scalability_mask":           fmt.Sprintf("0x%04x", oinf.ScalabilityMask),
			"profile_tier_levels_length": len(oinf.ProfileTierLevels),
		}

		if !opts.Json {
			fmt.Fprintf(w, "  oinf (Operating Points Information):\n")
			fmt.Fprintf(w, "    ScalabilityMask: 0x%04x\n", oinf.ScalabilityMask)
			fmt.Fprintf(w, "    ProfileTierLevels: %d\n", len(oinf.ProfileTierLevels))
		}

		var ptls []map[string]interface{}
		for j, ptl := range oinf.ProfileTierLevels {
			ptls = append(ptls, map[string]interface{}{
				"index":         j,
				"profile_space": ptl.GeneralProfileSpace,
				"tier_flag":     ptl.GeneralTierFlag,
				"profile_idc":   ptl.GeneralProfileIDC,
				"level_idc":     ptl.GeneralLevelIDC,
			})
			if !opts.Json {
				fmt.Fprintf(w, "      PTL[%d]: space=%d tier=%t profile=%d level=%d\n",
					j, ptl.GeneralProfileSpace, ptl.GeneralTierFlag,
					ptl.GeneralProfileIDC, ptl.GeneralLevelIDC)
			}
		}
		entryInfo["profile_tier_levels"] = ptls

		entryInfo["operating_points_length"] = len(oinf.OperatingPoints)
		if !opts.Json {
			fmt.Fprintf(w, "    OperatingPoints: %d\n", len(oinf.OperatingPoints))
		}

		var operatingPoints []map[string]interface{}
		for j, op := range oinf.OperatingPoints {
			opInfo := map[string]interface{}{
				"index":                j,
				"output_layer_set_idx": op.OutputLayerSetIdx,
				"max_temporal_id":      op.MaxTemporalID,
				"num_layers":           len(op.Layers),
				"min_pic_width":        op.MinPicWidth,
				"min_pic_height":       op.MinPicHeight,
				"max_pic_width":        op.MaxPicWidth,
				"max_pic_height":       op.MaxPicHeight,
			}
			if !opts.Json {
				fmt.Fprintf(w, "      OP[%d]: olsIdx=%d maxTid=%d layers=%d dims=%dx%d-%dx%d\n",
					j, op.OutputLayerSetIdx, op.MaxTemporalID, len(op.Layers),
					op.MinPicWidth, op.MinPicHeight, op.MaxPicWidth, op.MaxPicHeight)
			}

			var layers []map[string]interface{}
			for k, l := range op.Layers {
				layers = append(layers, map[string]interface{}{
					"index":           k,
					"ptl_idx":         l.PtlIdx,
					"layer_id":        l.LayerID,
					"is_output_layer": l.IsOutputLayer,
				})
				if !opts.Json {
					fmt.Fprintf(w, "        layer[%d]: ptlIdx=%d layerId=%d output=%t\n",
						k, l.PtlIdx, l.LayerID, l.IsOutputLayer)
				}
			}

			opInfo["layers"] = layers
			operatingPoints = append(operatingPoints, opInfo)
		}
		entryInfo["operating_points"] = operatingPoints

		if !opts.Json {
			fmt.Fprintf(w, "    DependencyLayers: %d\n", len(oinf.DependencyLayers))
		}

		entryInfo["dependency_layers_length"] = len(oinf.DependencyLayers)
		var dependencyLayers []map[string]interface{}
		for j, dep := range oinf.DependencyLayers {
			if !opts.Json {
				fmt.Fprintf(w, "      Dep[%d]: layerId=%d dependsOn=%v dimIds=%v\n",
					j, dep.LayerID, dep.DependsOnLayers, dep.DimensionIds)
			}

			dependencyLayers = append(dependencyLayers, map[string]interface{}{
				"index":             j,
				"layer_id":          dep.LayerID,
				"depends_on_layers": dep.DependsOnLayers,
				"dimension_ids":     dep.DimensionIds,
			})
		}
		entryInfo["dependency_layers"] = dependencyLayers
		oinfEntries = append(oinfEntries, entryInfo)
	}
	return oinfEntries
}

func printLinfInfo(sgpd *mp4.SgpdBox, opts InfoOptions, w io.Writer) []map[string]interface{} {

	var linfEntries []map[string]interface{}

	for _, entry := range sgpd.SampleGroupEntries {
		linf, ok := entry.(*mp4.LinfSampleGroupEntry)
		if !ok {
			continue
		}
		if !opts.Json {
			fmt.Fprintf(w, "  linf (Layer Information):\n")
		}

		entryInfo := map[string]interface{}{}
		var layers []map[string]interface{}

		for j, l := range linf.Layers {
			if !opts.Json {
				fmt.Fprintf(w, "    Layer[%d]: layerId=%d minTid=%d maxTid=%d subFlags=0x%02x\n",
					j, l.LayerID, l.MinTemporalID, l.MaxTemporalID, l.SubLayerPresenceFlags)
			}

			layerInfo := map[string]interface{}{
				"index":                    j,
				"layer_id":                 l.LayerID,
				"min_temporal_id":          l.MinTemporalID,
				"max_temporal_id":          l.MaxTemporalID,
				"sub_layer_presence_flags": fmt.Sprintf("0x%02x", l.SubLayerPresenceFlags),
			}

			layers = append(layers, layerInfo)
		}

		entryInfo["layers"] = layers
		linfEntries = append(linfEntries, entryInfo)
	}

	return linfEntries
}

func printTrackGroupInfo(trak *mp4.TrakBox, opts InfoOptions, w io.Writer) []map[string]interface{} {
	if trak.Trgr == nil {
		return nil
	}
	if !opts.Json {
		fmt.Fprintf(w, "  trgr (Track Group):\n")
	}

	var groups []map[string]interface{}
	for _, child := range trak.Trgr.Children {
		if cstg, ok := child.(*mp4.TrackGroupTypeBox); ok {
			if !opts.Json {
				fmt.Fprintf(w, "    %s: trackGroupID=%d\n", cstg.Type(), cstg.TrackGroupID)
			}
			groups = append(groups, map[string]interface{}{
				"type":           cstg.Type(),
				"track_group_id": cstg.TrackGroupID,
			})
		}
	}
	return groups
}

func printFragmentedInfo(f *mp4.File, trackID uint32, timeScale uint32, opts InfoOptions, w io.Writer) map[string]interface{} {
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

	res := make(map[string]interface{})
	res["samples"] = totalSamples
	res["timescale"] = timeScale

	if sampleDur > 0 {
		fps := float64(timeScale) / float64(sampleDur)
		res["sample_duration"] = sampleDur
		res["fps"] = fps
		if !opts.Json {
			fmt.Fprintf(w, "  Samples: %d, Timescale: %d, SampleDur: %d (%.3f fps)\n",
				totalSamples, timeScale, sampleDur, fps)
		}
	} else {
		if !opts.Json {
			fmt.Fprintf(w, "  Samples: %d, Timescale: %d\n", totalSamples, timeScale)
		}
	}

	if opts.ShowIDR && len(syncFrames) > 0 {
		res["sync_frames"] = printSyncFrameInfo(syncFrames, opts, w)
	}
	return res
}
