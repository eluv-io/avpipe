package mp4e

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/mp4"

	"github.com/eluv-io/errors-go"
)

type Mp4Info struct {
	Errors            []string      // problems found
	FragmentCount     uint64        // total number of fragments
	InitInfo          *InitInfo     // moov
	SampleCount       uint64        // total number of samples/frames
	SampleCountMax    uint64        // most number of samples in a segment
	SampleCountMin    uint64        // least number of samples in a segment
	SampleDurationMax uint64        // longest sample duration
	SampleDurationMin uint64        // shortest sample duration
	Segments          []SegmentInfo //
	Size              uint64        // file size
	Timescale         uint32        //
}

// AddError saves errors for handling at the end of validation. The seg, frag,
// and pts fields provide a way to conveniently add some context.
func (s *Mp4Info) AddError(err string, seg, frag int, pts *uint64) {
	seg++
	frag++

	if seg > 0 {
		err += fmt.Sprintf(", segment %d", seg)
	}
	if frag > 0 {
		err += fmt.Sprintf(", fragment %d", frag)
	}
	if pts != nil {
		err += fmt.Sprintf(", pts %d", *pts)
	}

	s.Errors = append(s.Errors, err)
}

func (s *Mp4Info) String() string {
	var sb strings.Builder
	if len(s.Errors) > 0 {
		b, _ := json.MarshalIndent(s.Errors, "", "  ")
		_, _ = fmt.Fprintf(&sb, "errors: %s\n", string(b))
	}
	_, _ = fmt.Fprintf(&sb,
		"size: %d\ntimescale: %d\nfragments: %d, samples/frames: %d\nsample duration range: %d - %d\nsamples per segment: %d - %d\n\n",
		s.Size, s.Timescale,
		s.FragmentCount, s.SampleCount,
		s.SampleDurationMin, s.SampleDurationMax,
		s.SampleCountMin, s.SampleCountMax,
	)
	for i, seg := range s.Segments {
		_, _ = fmt.Fprintf(&sb, "%d: dts: %d - %d, pts %d - %d, seq %d - %d, samples: %d\n",
			i+1, seg.DtsStart, seg.DtsEnd, seg.PtsStart, seg.PtsEnd, seg.SeqStart, seg.SeqEnd, seg.SampleCount)
	}
	return sb.String()
}

type InitInfo struct {
}
type SegmentInfo struct {
	DtsEnd        uint64
	DtsStart      uint64
	FragmentCount uint64
	PtsEnd        uint64
	PtsStart      uint64
	SampleCount   uint64
	Samples       []*SampleInfo
	SeqEnd        uint32
	SeqStart      uint32
}

type SampleInfo struct {
	Duration uint64
	Fragment int
	Pts      uint64
}

func ValidateFmp4(reader io.Reader) (file *mp4.File, info *Mp4Info, err error) {
	e := errors.Template("mp4e.ValidateFmp4", errors.K.Invalid.Default())

	info = &Mp4Info{}

	file, err = mp4.DecodeFile(reader)
	if file == nil {
		err = e("reason", "DecodeFile failed", "error", err)
		return
	}
	if err != nil {
		info.AddError(fmt.Sprintf("failed to completely parse MP4: %v", err), -1, -1, nil)
		err = nil
	}

	info.Size = file.Size()

	if file.Init != nil {
		// moov - container for all the metadata
		//   trak - container for an individual track or stream
		//     tkhd - track header, overall information about the track; height, width
		//     mdia - container for the media information in a track
		//       mdhd - media header, overall information about the media; timescale
		//       minf - media information container
		//         stbl - sample table box, container for the time/space map
		//           stsd - sample descriptions (codec types, initialization etc.); codec info
		// We may want to check file.Moov.Trak.Mdia.Minf.Stbl.Stsd.AvcX.AvcC (and HEVC)
		info.InitInfo = &InitInfo{}
	}
	if len(file.Segments) == 0 {
		if file.Init == nil {
			info.AddError("No segments", -1, -1, nil)
		}
		return
	}

	if !file.IsFragmented() {
		info.AddError("Not fragmented", -1, -1, nil)
	}
	if file.Segments[0].Sidx != nil {
		info.Timescale = file.Segments[0].Sidx.Timescale
	}

	dtsPrev := uint64(0)
	ptsPrev := uint64(0)
	sampleCountPrev := uint64(0)
	sampleCountPrevPrev := uint64(0)
	seqPrev := uint32(0)
	for segIdx, seg := range file.Segments {
		info.FragmentCount += uint64(len(seg.Fragments))
		segInfo := SegmentInfo{
			FragmentCount: uint64(len(seg.Fragments)),
		}

		if seg.Fragments == nil {
			info.AddError("No fragments", segIdx, -1, nil)
		}

		// moof - movie fragment
		//   mfhd - movie fragment header; sequence number
		//   traf - track fragment
		//     tfhd - track fragment header; track ID, default sample duration
		//     tfdt - track fragment decode time
		for fragIdx, frag := range seg.Fragments {
			seq := uint32(0)

			if frag.Moof == nil {
				info.AddError("No moof", segIdx, fragIdx, nil)
			} else {
				if frag.Moof.Mfhd == nil {
					info.AddError("No mfhd", segIdx, fragIdx, nil)
				} else {
					seq = frag.Moof.Mfhd.SequenceNumber
				}

				//if frag.Moof.Traf == nil {
				//	info.AddError("No traf", segIdx, fragIdx)
				//} else {
				//	if frag.Moof.Traf.Tfhd == nil {
				//		info.AddError("No tfhd", segIdx, fragIdx)
				//	} else {
				//		dur = uint64(frag.Moof.Traf.Tfhd.DefaultSampleDuration)
				//		if dur == 0 {
				//			info.AddError("default_sample_duration is 0", segIdx, fragIdx)
				//		}
				//	}
				//	if frag.Moof.Traf.Tfdt == nil {
				//		info.AddError("No tfdt", segIdx, fragIdx)
				//	} else {
				//		dts = frag.Moof.Traf.Tfdt.BaseMediaDecodeTime()
				//	}
				//}
			}
			//if len(seg.Sidxs) > fragIdx {
			//	sidx := seg.Sidxs[fragIdx]
			//	pts = sidx.EarliestPresentationTime
			//}

			if fragIdx == 0 {
				segInfo.SeqStart = seq
			} else if fragIdx == len(seg.Fragments)-1 {
				segInfo.SeqEnd = seq
			}

			// Check for gaps in sequence number
			if seq != seqPrev+1 && fragIdx > 0 {
				info.AddError(fmt.Sprintf("Sequence number gap %d - %d", seqPrev, seq), segIdx, fragIdx, nil)
			}
			seqPrev = seq

			var samples []mp4.FullSample
			samples, err = frag.GetFullSamples(nil)
			if err != nil {
				info.AddError(fmt.Sprintf("failed to read samples: %v", err), segIdx, fragIdx, nil)
				continue
			}
			sampleCount := uint64(len(samples))
			segInfo.SampleCount += sampleCount
			for sampleIdx, sample := range samples {
				dur := uint64(sample.Dur)
				if dur < info.SampleDurationMin || info.SampleDurationMin == 0 {
					info.SampleDurationMin = dur
				}
				if dur > info.SampleDurationMax {
					info.SampleDurationMax = dur
				}
				dts := sample.DecodeTime
				pts := sample.PresentationTime()

				// Build list of samples
				segInfo.Samples = append(segInfo.Samples, &SampleInfo{dur, fragIdx, pts})

				// Check for IDR frames (video only)
				if fragIdx == 0 && sampleIdx == 0 {
					segInfo.DtsStart = dts
					segInfo.PtsStart = pts
					if err = ensureIDRFrame(sample); err != nil {
						info.AddError(err.Error(), segIdx, fragIdx, &pts)
					}
				} else if fragIdx == len(seg.Fragments)-1 && sampleIdx == len(samples)-1 {
					segInfo.DtsEnd = dts + dur
					segInfo.PtsEnd = pts + dur
				}

				// Check for gaps in DTS. DTS should come in order
				if dtsPrev != 0 && absDiff(dts-dtsPrev, dur) > 0 {
					info.AddError(fmt.Sprintf("DTS gap %d - %d, sample duration %d", dtsPrev, dts, dur), segIdx, fragIdx, &pts)
				}
				dtsPrev = dts
			}
		}

		// Sort because PTS may not be in order
		sort.Slice(segInfo.Samples, func(a, b int) bool {
			return segInfo.Samples[a].Pts < segInfo.Samples[b].Pts
		})

		// Check for gaps in PTS
		for _, s := range segInfo.Samples {
			if ptsPrev != 0 && absDiff(s.Pts-ptsPrev, s.Duration) > 0 {
				info.AddError(fmt.Sprintf("PTS gap %d - %d, sample duration %d", ptsPrev, s.Pts, s.Duration), segIdx, s.Fragment, nil)
			}
			ptsPrev = s.Pts
		}

		// Check sample counts per segment
		info.SampleCount += segInfo.SampleCount
		if isSampleCountSequenceBad(sampleCountPrevPrev, sampleCountPrev, segInfo.SampleCount) &&
			segIdx < len(file.Segments)-1 { // ignore last segment
			info.AddError(fmt.Sprintf("Sample count mismatch %d - %d", sampleCountPrev, segInfo.SampleCount), segIdx, -1, nil)
		}
		sampleCountPrevPrev = sampleCountPrev
		sampleCountPrev = segInfo.SampleCount
		if segInfo.SampleCount < info.SampleCountMin || info.SampleCountMin == 0 {
			info.SampleCountMin = segInfo.SampleCount
		}
		if segInfo.SampleCount > info.SampleCountMax {
			info.SampleCountMax = segInfo.SampleCount
		}
		info.Segments = append(info.Segments, segInfo)
	}

	return file, info, nil
}

func absDiff(x, y uint64) uint64 {
	if x < y {
		return y - x
	}
	return x - y
}

// ensureIDRFrame returns nil if the sample contains an IDR frame, or is not
// a video sample. Assumes AVC encoding
func ensureIDRFrame(sample mp4.FullSample) (err error) {
	nalus, err := avc.GetNalusFromSample(sample.Data)
	if err != nil {
		if strings.Index(err.Error(), "Not video?") != -1 {
			err = nil
		}
		return
	}

	err = fmt.Errorf("IDR random access slice NAL unit not found")
	for _, nalu := range nalus {
		naluType := avc.GetNaluType(nalu[0])
		switch naluType {
		case avc.NALU_IDR:
			err = nil
			break
		case avc.NALU_NON_IDR:
			err = fmt.Errorf("non-IDR slice NAL unit found")
			break
		default:
			// skip SEI, SPS, PPS, etc
		}
	}
	return
}

// isSampleCountSequenceBad checks for unexpected variation in sample count
// between segments. Three segments are required for validation to accomodate
// "partial" segments generated by splice points, i.e. one segment split into
// two segments that together contain the same samples in the original segment.
// A simpler alternative would be to specify the expected sample count. Also
// note that this assumes one frame per sample, which is not always the case.
func isSampleCountSequenceBad(a, b, c uint64) bool {
	valid := (a == b && b == c) || // 50 50 50 - expected
		a == b || // 50 50 14 - c is either bad, a partial segment, or the last segment
		a == b+c || // 50 14 36 - b and c are partial segments
		a+b == c || // 14 36 50 - a and b are partial segments
		b == c // 36 50 50 - c is a partial segment or bad
	// 50 48 50 - prev bad (prev of prev bad handled above)
	// 50 48 51 or 48 51 50 - two bad
	// 0 0 50 or 0 50 50 - first segments handled above
	// 0 49 50 - bad first segment (this is not true in all cases but good enough)
	// 14 36 22 28 - consecutive partials not handled
	return !valid
}
