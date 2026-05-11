package mvhevc

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
)

type InfoOptions struct {
	ShowIDR bool
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

	for i, trak := range parsedMP4.Moov.Traks {
		fmt.Fprintf(w, "Track %d (ID=%d):\n", i+1, trak.Tkhd.TrackID)

		stbl := trak.Mdia.Minf.Stbl
		if stbl == nil || stbl.Stsd == nil {
			continue
		}

		for _, child := range stbl.Stsd.Children {
			vse, ok := child.(*mp4.VisualSampleEntryBox)
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  Sample entry: %s (%dx%d)\n", vse.Type(), vse.Width, vse.Height)

			if vse.HvcC != nil {
				fmt.Fprintf(w, "  hvcC (base layer config):\n")
				hdcr := vse.HvcC.DecConfRec
				fmt.Fprintf(w, "    Profile: space=%d tier=%t idc=%d level=%d\n",
					hdcr.GeneralProfileSpace, hdcr.GeneralTierFlag,
					hdcr.GeneralProfileIDC, hdcr.GeneralLevelIDC)
				fmt.Fprintf(w, "    Chroma: %d  BitDepth: luma=%d chroma=%d\n",
					hdcr.ChromaFormatIDC,
					hdcr.BitDepthLumaMinus8+8,
					hdcr.BitDepthChromaMinus8+8)
				fmt.Fprintf(w, "    NumTemporalLayers: %d  LengthSize: %d\n",
					hdcr.NumTemporalLayers, hdcr.LengthSizeMinusOne+1)

				vpsNalus := hdcr.GetNalusForType(hevc.NALU_VPS)
				if len(vpsNalus) > 0 {
					vps, err := hevc.ParseVPSNALUnit(vpsNalus[0])
					if err != nil {
						fmt.Fprintf(w, "    VPS parse error: %v\n", err)
					} else {
						fmt.Fprintf(w, "    VPS: layers=%d views=%d multiLayer=%t\n",
							vps.GetNumLayers(), vps.GetNumViews(), vps.IsMultiLayer())
					}
				}

				for _, array := range hdcr.NaluArrays {
					fmt.Fprintf(w, "    %s: %d nalus (complete=%d)\n",
						array.NaluType(), len(array.Nalus), array.Complete())
				}
			}

			if vse.LhvC != nil {
				fmt.Fprintf(w, "  lhvC (enhancement layer config):\n")
				hdcr := vse.LhvC.DecConfRec
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

			if vse.Vexu != nil {
				fmt.Fprintf(w, "  vexu (Spatial Video):\n")
				if vse.Vexu.Eyes != nil {
					eyes := vse.Vexu.Eyes
					if eyes.Stri != nil {
						fmt.Fprintf(w, "    stri: left=%t right=%t reversed=%t\n",
							eyes.Stri.HasLeftEye(),
							eyes.Stri.HasRightEye(),
							eyes.Stri.EyeViewsReversed())
					}
					if eyes.Hero != nil {
						fmt.Fprintf(w, "    hero: %s (%d)\n",
							eyes.Hero.HeroEyeName(), eyes.Hero.HeroEye)
					}
					if eyes.Cams != nil && eyes.Cams.Blin != nil {
						fmt.Fprintf(w, "    baseline: %d um (%.1f mm)\n",
							eyes.Cams.Blin.Baseline,
							float64(eyes.Cams.Blin.Baseline)/1000.0)
					}
				}
				if vse.Vexu.Proj != nil && vse.Vexu.Proj.Prji != nil {
					fmt.Fprintf(w, "    projection: %s\n", vse.Vexu.Proj.Prji.ProjectionType)
				}
			}

			if vse.Hfov != nil {
				fmt.Fprintf(w, "  hfov: %d/1000 degrees (%.1f)\n",
					vse.Hfov.FieldOfView,
					float64(vse.Hfov.FieldOfView)/1000.0)
			}
		}

		timeScale := trak.Mdia.Mdhd.Timescale
		trackID := trak.Tkhd.TrackID

		if parsedMP4.IsFragmented() {
			printFragmentedInfo(parsedMP4, trackID, timeScale, opts.ShowIDR, w)
		} else {
			nrSamples := trak.GetNrSamples()
			var sampleDur uint32
			if stbl.Stts != nil && len(stbl.Stts.SampleTimeDelta) > 0 {
				sampleDur = stbl.Stts.SampleTimeDelta[0]
			}
			if sampleDur > 0 {
				fps := float64(timeScale) / float64(sampleDur)
				fmt.Fprintf(w, "  Samples: %d, Timescale: %d, SampleDur: %d (%.3f fps)\n",
					nrSamples, timeScale, sampleDur, fps)
			} else {
				fmt.Fprintf(w, "  Samples: %d, Timescale: %d\n", nrSamples, timeScale)
			}
			if stbl.Stss != nil && opts.ShowIDR {
				syncNrs := stbl.Stss.SampleNumber
				fmt.Fprintf(w, "  Sync (IDR) frames (%d):\n", len(syncNrs))
				for i, sn := range syncNrs {
					if i == 0 {
						fmt.Fprintf(w, "    frame %d\n", sn)
					} else {
						interval := sn - syncNrs[i-1]
						fmt.Fprintf(w, "    frame %d (interval=%d)\n", sn, interval)
					}
				}
			}
		}

		for _, child := range stbl.Children {
			sgpd, ok := child.(*mp4.SgpdBox)
			if !ok {
				continue
			}
			switch sgpd.GroupingType {
			case "oinf":
				printOinfInfo(sgpd, w)
			case "linf":
				printLinfInfo(sgpd, w)
			}
		}

		if trak.Trgr != nil {
			fmt.Fprintf(w, "  trgr (Track Group):\n")
			for _, child := range trak.Trgr.Children {
				if cstg, ok := child.(*mp4.TrackGroupTypeBox); ok {
					fmt.Fprintf(w, "    %s: trackGroupID=%d\n", cstg.Type(), cstg.TrackGroupID)
				}
			}
		}
	}

	return nil
}

func printOinfInfo(sgpd *mp4.SgpdBox, w io.Writer) {
	for _, entry := range sgpd.SampleGroupEntries {
		oinf, ok := entry.(*mp4.OinfSampleGroupEntry)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "  oinf (Operating Points Information):\n")
		fmt.Fprintf(w, "    ScalabilityMask: 0x%04x\n", oinf.ScalabilityMask)
		fmt.Fprintf(w, "    ProfileTierLevels: %d\n", len(oinf.ProfileTierLevels))
		for j, ptl := range oinf.ProfileTierLevels {
			fmt.Fprintf(w, "      PTL[%d]: space=%d tier=%t profile=%d level=%d\n",
				j, ptl.GeneralProfileSpace, ptl.GeneralTierFlag,
				ptl.GeneralProfileIDC, ptl.GeneralLevelIDC)
		}
		fmt.Fprintf(w, "    OperatingPoints: %d\n", len(oinf.OperatingPoints))
		for j, op := range oinf.OperatingPoints {
			fmt.Fprintf(w, "      OP[%d]: olsIdx=%d maxTid=%d layers=%d dims=%dx%d-%dx%d\n",
				j, op.OutputLayerSetIdx, op.MaxTemporalID, len(op.Layers),
				op.MinPicWidth, op.MinPicHeight, op.MaxPicWidth, op.MaxPicHeight)
			for k, l := range op.Layers {
				fmt.Fprintf(w, "        layer[%d]: ptlIdx=%d layerId=%d output=%t\n",
					k, l.PtlIdx, l.LayerID, l.IsOutputLayer)
			}
		}
		fmt.Fprintf(w, "    DependencyLayers: %d\n", len(oinf.DependencyLayers))
		for j, dep := range oinf.DependencyLayers {
			fmt.Fprintf(w, "      Dep[%d]: layerId=%d dependsOn=%v dimIds=%v\n",
				j, dep.LayerID, dep.DependsOnLayers, dep.DimensionIds)
		}
	}
}

func printLinfInfo(sgpd *mp4.SgpdBox, w io.Writer) {
	for _, entry := range sgpd.SampleGroupEntries {
		linf, ok := entry.(*mp4.LinfSampleGroupEntry)
		if !ok {
			continue
		}
		fmt.Fprintf(w, "  linf (Layer Information):\n")
		for j, l := range linf.Layers {
			fmt.Fprintf(w, "    Layer[%d]: layerId=%d minTid=%d maxTid=%d subFlags=0x%02x\n",
				j, l.LayerID, l.MinTemporalID, l.MaxTemporalID, l.SubLayerPresenceFlags)
		}
	}
}

func printFragmentedInfo(f *mp4.File, trackID uint32, timeScale uint32, showIDR bool, w io.Writer) {
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

	if sampleDur > 0 {
		fps := float64(timeScale) / float64(sampleDur)
		fmt.Fprintf(w, "  Samples: %d, Timescale: %d, SampleDur: %d (%.3f fps)\n",
			totalSamples, timeScale, sampleDur, fps)
	} else {
		fmt.Fprintf(w, "  Samples: %d, Timescale: %d\n", totalSamples, timeScale)
	}

	if showIDR && len(syncFrames) > 0 {
		fmt.Fprintf(w, "  Sync (IDR) frames (%d):\n", len(syncFrames))
		for i, sn := range syncFrames {
			if i == 0 {
				fmt.Fprintf(w, "    frame %d\n", sn)
			} else {
				interval := sn - syncFrames[i-1]
				fmt.Fprintf(w, "    frame %d (interval=%d)\n", sn, interval)
			}
		}
	}
}
