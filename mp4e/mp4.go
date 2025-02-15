package mp4e

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

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

type InitInfo struct {
}
type SegmentInfo struct {
	DtsEnd        uint64
	DtsStart      uint64
	FragmentCount uint64
	PtsEnd        uint64
	PtsSamples    []*PtsSample
	PtsStart      uint64
	SampleCount   uint64
	SeqEnd        uint32
	SeqStart      uint32
}

type PtsSample struct {
	Duration uint64
	Fragment int
	Pts      uint64
}

func (s *Mp4Info) AddError(err string, seg, frag int) {
	seg++
	frag++
	if seg == 0 {
		s.Errors = append(s.Errors, err)
	} else if frag == 0 {
		s.Errors = append(s.Errors, fmt.Sprintf("%s, in segment %d", err, seg))
	} else {
		s.Errors = append(s.Errors, fmt.Sprintf("%s, in segment %d fragment %d", err, seg, frag))
	}
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

func ParseFmp4(reader io.Reader) (file *mp4.File, info *Mp4Info, err error) {
	e := errors.Template("mp4.ParseFmp4", errors.K.Invalid.Default())
	if file, err = mp4.DecodeFile(reader); err != nil {
		err = e(err)
		return
	}

	info = &Mp4Info{
		Size: file.Size(),
	}

	if file.Init != nil {
		// moov - container for all the metadata
		//   trak - container for an individual track or stream
		//     tkhd - track header, overall information about the track; height, width
		//     mdia - container for the media information in a track
		//       mdhd - media header, overall information about the media; timescale
		//       minf - media information container
		//         stbl - sample table box, container for the time/space map
		//           stsd - sample descriptions (codec types, initialization etc.); codec info
		// file.Moov.Trak.Mdia.Minf.Stbl.Stsd.AvcX.AvcC
		info.InitInfo = &InitInfo{}
	}
	if len(file.Segments) == 0 {
		if file.Init == nil {
			info.AddError("No segments", -1, -1)
		}
		return
	}

	if !file.IsFragmented() {
		info.AddError("Not fragmented", -1, -1)
	}
	if file.Segments[0].Sidx != nil {
		info.Timescale = file.Segments[0].Sidx.Timescale
	}

	dtsPrev := uint64(0)
	ptsPrev := uint64(0)
	sampleCountPrev := uint64(0)
	seqPrev := uint32(0)
	for i, seg := range file.Segments {
		info.FragmentCount += uint64(len(seg.Fragments))
		segStats := SegmentInfo{
			FragmentCount: uint64(len(seg.Fragments)),
		}

		if seg.Fragments == nil {
			info.AddError("No fragments", i, -1)
		}

		// moof - movie fragment
		//   mfhd - movie fragment header; sequence number
		//   traf - track fragment
		//     tfhd - track fragment header; track ID, default sample duration
		//     tfdt - track fragment decode time
		for j, frag := range seg.Fragments {
			seq := uint32(0)

			if frag.Moof == nil {
				info.AddError("No moof", i, j)
			} else {
				if frag.Moof.Mfhd == nil {
					info.AddError("No mfhd", i, j)
				} else {
					seq = frag.Moof.Mfhd.SequenceNumber
				}

				//if frag.Moof.Traf == nil {
				//	info.AddError("No traf", i, j)
				//} else {
				//	if frag.Moof.Traf.Tfhd == nil {
				//		info.AddError("No tfhd", i, j)
				//	} else {
				//		dur = uint64(frag.Moof.Traf.Tfhd.DefaultSampleDuration)
				//		if dur == 0 {
				//			info.AddError("default_sample_duration is 0", i, j)
				//		}
				//	}
				//	if frag.Moof.Traf.Tfdt == nil {
				//		info.AddError("No tfdt", i, j)
				//	} else {
				//		dts = frag.Moof.Traf.Tfdt.BaseMediaDecodeTime()
				//	}
				//}
			}
			//if len(seg.Sidxs) > j {
			//	sidx := seg.Sidxs[j]
			//	pts = sidx.EarliestPresentationTime
			//}

			if j == 0 {
				segStats.SeqStart = seq
			} else if j == len(seg.Fragments)-1 {
				segStats.SeqEnd = seq
			}

			if seq != seqPrev+1 {
				info.AddError(fmt.Sprintf("Sequence number gap %d - %d", seqPrev, seq), i, j)
			}
			seqPrev = seq

			var samples []mp4.FullSample
			samples, err = frag.GetFullSamples(nil)
			if err != nil {
				info.AddError(fmt.Sprintf("failed to read samples: %v", err), i, j)
				continue
			}
			sampleCount := uint64(len(samples))
			segStats.SampleCount += sampleCount
			for k, sample := range samples {
				dur := uint64(sample.Dur)
				if dur < info.SampleDurationMin || info.SampleDurationMin == 0 {
					info.SampleDurationMin = dur
				}
				if dur > info.SampleDurationMax {
					info.SampleDurationMax = dur
				}
				dts := sample.DecodeTime
				pts := sample.PresentationTime()
				segStats.PtsSamples = append(segStats.PtsSamples, &PtsSample{dur, j, pts})
				if j == 0 && k == 0 {
					segStats.DtsStart = dts
					segStats.PtsStart = pts
				} else if j == len(seg.Fragments)-1 && k == len(samples)-1 {
					segStats.DtsEnd = dts + dur
					segStats.PtsEnd = pts + dur
				}
				if dtsPrev != 0 && absDiff(dts-dtsPrev, dur) > 1 {
					info.AddError(fmt.Sprintf("DTS gap %d - %d, sample duration %d", dtsPrev, dts, dur), i, j)
				}
				dtsPrev = dts
			}
		}

		// PTS may not be in order
		sort.Slice(segStats.PtsSamples, func(a, b int) bool {
			return segStats.PtsSamples[a].Pts < segStats.PtsSamples[b].Pts
		})
		for _, s := range segStats.PtsSamples {
			if ptsPrev != 0 && absDiff(s.Pts-ptsPrev, s.Duration) > 1 {
				info.AddError(fmt.Sprintf("PTS gap %d - %d, sample duration %d", ptsPrev, s.Pts, s.Duration), i, s.Fragment)
			}
			ptsPrev = s.Pts
		}

		info.SampleCount += segStats.SampleCount
		if sampleCountPrev != segStats.SampleCount && sampleCountPrev != 0 && i < len(file.Segments)-1 {
			info.AddError(fmt.Sprintf("Sample count mismatch %d - %d (expected for partials)", sampleCountPrev, segStats.SampleCount), i, -1)
		}
		sampleCountPrev = segStats.SampleCount
		if segStats.SampleCount < info.SampleCountMin || info.SampleCountMin == 0 {
			info.SampleCountMin = segStats.SampleCount
		}
		if segStats.SampleCount > info.SampleCountMax {
			info.SampleCountMax = segStats.SampleCount
		}
		info.Segments = append(info.Segments, segStats)
	}

	return file, info, nil
}

func WriteSegmentInfo(rd io.Reader, wr io.Writer) error {

	pmp4, _, err := ParseFmp4(rd)
	if err != nil {
		return err
	}

	err = pmp4.Info(wr, "", "", "  ")
	if err != nil {
		return err
	}

	return nil
}

func absDiff(x, y uint64) uint64 {
	if x < y {
		return y - x
	}
	return x - y
}
