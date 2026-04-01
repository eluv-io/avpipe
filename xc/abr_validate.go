package xc

import (
	"bytes"
	"fmt"
	"os"

	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/errors-go"
)

// ABRSegmentResult holds the results of ABR segment validation.
type ABRSegmentResult struct {
	Errors      []string
	FrameCount  int
	FragCount   int
	Timescale   uint32
	SampleDur   uint64 // most common sample duration
	DtsStart    uint64
	DtsEnd      uint64
	DtsProblems int    // non-consecutive DTS count
	DurProblems int    // unequal duration count
	SeqFirst    uint32 // first mfhd sequence number
	SeqLast     uint32 // last mfhd sequence number
}

// Valid returns true if no errors were found.
func (r *ABRSegmentResult) Valid() bool {
	return len(r.Errors) == 0 && r.DtsProblems == 0 && r.DurProblems == 0
}

// AllErrors returns a combined error with all issues, or nil if valid.
func (r *ABRSegmentResult) AllErrors() error {
	if r.Valid() {
		return nil
	}
	e := errors.Template("xc.ABRSegmentResult",
		"frames", r.FrameCount, "fragments", r.FragCount,
		"dts_problems", r.DtsProblems, "dur_problems", r.DurProblems)
	msg := fmt.Sprintf("%d errors, %d DTS problems, %d duration problems",
		len(r.Errors), r.DtsProblems, r.DurProblems)
	for _, err := range r.Errors {
		msg += "\n  " + err
	}
	return e(fmt.Errorf("%s", msg))
}

// ValidateABRSegment validates a DASH/HLS segment (m4s file).
// Checks:
//   - consecutive DTS (each sample DTS = previous DTS + duration)
//   - equal frame durations across all samples
//   - reports frame and fragment counts for the caller to check
func ValidateABRSegment(filename string) (*ABRSegmentResult, error) {
	e := errors.Template("xc.ValidateABRSegment", "filename", filename)

	f, err := os.Open(filename)
	if err != nil {
		return nil, e(err)
	}
	defer f.Close()

	parsed, err := mp4.DecodeFile(f)
	if err != nil {
		return nil, e(fmt.Errorf("failed to parse MP4: %w", err))
	}

	result := &ABRSegmentResult{}

	// Extract timescale from init if present, otherwise from first traf
	if parsed.Moov != nil && len(parsed.Moov.Traks) > 0 {
		result.Timescale = parsed.Moov.Traks[0].Mdia.Mdhd.Timescale
	}

	// Collect all samples
	type sample struct {
		DTS uint64
		Dur uint64
	}
	var samples []sample

	for _, seg := range parsed.Segments {
		for _, frag := range seg.Fragments {
			result.FragCount++

			// Track sequence numbers from mfhd
			if frag.Moof != nil && frag.Moof.Mfhd != nil {
				seq := frag.Moof.Mfhd.SequenceNumber
				if result.FragCount == 1 {
					result.SeqFirst = seq
				}
				result.SeqLast = seq
			}

			fullSamples, sErr := frag.GetFullSamples(nil)
			if sErr != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("failed to read samples: %v", sErr))
				continue
			}
			for _, s := range fullSamples {
				samples = append(samples, sample{
					DTS: s.DecodeTime,
					Dur: uint64(s.Dur),
				})
			}
		}
	}

	result.FrameCount = len(samples)
	if len(samples) == 0 {
		result.Errors = append(result.Errors, "no samples found")
		return result, nil
	}

	result.DtsStart = samples[0].DTS
	last := samples[len(samples)-1]
	result.DtsEnd = last.DTS + last.Dur
	result.SampleDur = samples[0].Dur

	// Check consecutive DTS and equal durations
	for i := 1; i < len(samples); i++ {
		expectedDTS := samples[i-1].DTS + samples[i-1].Dur
		if samples[i].DTS != expectedDTS {
			result.DtsProblems++
			if result.DtsProblems <= 5 {
				result.Errors = append(result.Errors,
					fmt.Sprintf("DTS gap at sample %d: expected %d, got %d",
						i, expectedDTS, samples[i].DTS))
			}
		}
		if samples[i].Dur != result.SampleDur {
			result.DurProblems++
			if result.DurProblems <= 5 {
				result.Errors = append(result.Errors,
					fmt.Sprintf("duration mismatch at sample %d: expected %d, got %d",
						i, result.SampleDur, samples[i].Dur))
			}
		}
	}

	return result, nil
}

// InitSegmentInfo holds parsed properties from a DASH/HLS init segment.
type InitSegmentInfo struct {
	Codec     string
	Width     int
	Height    int
	Timescale uint32
	SPS       [][]byte // Sequence Parameter Sets
	PPS       [][]byte // Picture Parameter Sets
	VPS       [][]byte // Video Parameter Sets (HEVC only)
}

// ValidateInitSegment parses an init segment and extracts stream properties
// including SPS, PPS, and VPS (for MVHEVC).
func ValidateInitSegment(filename string) (*InitSegmentInfo, error) {
	e := errors.Template("xc.ValidateInitSegment", "filename", filename)

	f, err := os.Open(filename)
	if err != nil {
		return nil, e(err)
	}
	defer f.Close()

	parsed, err := mp4.DecodeFile(f)
	if err != nil {
		return nil, e(fmt.Errorf("failed to parse MP4: %w", err))
	}

	if parsed.Moov == nil || len(parsed.Moov.Traks) == 0 {
		return nil, e(fmt.Errorf("no moov/trak found"))
	}

	trak := parsed.Moov.Traks[0]
	info := &InitSegmentInfo{
		Timescale: trak.Mdia.Mdhd.Timescale,
	}

	stsd := trak.Mdia.Minf.Stbl.Stsd
	if stsd.AvcX != nil {
		info.Codec = "h264"
		if stsd.AvcX.AvcC != nil {
			info.SPS = stsd.AvcX.AvcC.SPSnalus
			info.PPS = stsd.AvcX.AvcC.PPSnalus
		}
	} else if stsd.HvcX != nil {
		info.Codec = "h265"
		if stsd.HvcX.HvcC != nil {
			info.VPS = stsd.HvcX.HvcC.GetNalusForType(hevc.NALU_VPS)
			info.SPS = stsd.HvcX.HvcC.GetNalusForType(hevc.NALU_SPS)
			info.PPS = stsd.HvcX.HvcC.GetNalusForType(hevc.NALU_PPS)
		}
	}

	if trak.Tkhd != nil {
		info.Width = int(trak.Tkhd.Width >> 16)
		info.Height = int(trak.Tkhd.Height >> 16)
	}

	return info, nil
}

// InitSegmentsMatch compares two init segments for codec compatibility.
// It checks codec type, dimensions, timescale, and SPS/PPS/VPS byte equality.
// Returns nil if they match, or an error describing the first mismatch.
func InitSegmentsMatch(a, b *InitSegmentInfo) error {
	if a.Codec != b.Codec {
		return fmt.Errorf("codec mismatch: %s vs %s", a.Codec, b.Codec)
	}
	if a.Width != b.Width || a.Height != b.Height {
		return fmt.Errorf("dimensions mismatch: %dx%d vs %dx%d", a.Width, a.Height, b.Width, b.Height)
	}
	if a.Timescale != b.Timescale {
		return fmt.Errorf("timescale mismatch: %d vs %d", a.Timescale, b.Timescale)
	}
	if err := naluSetsEqual("SPS", a.SPS, b.SPS); err != nil {
		return err
	}
	if err := naluSetsEqual("PPS", a.PPS, b.PPS); err != nil {
		return err
	}
	if err := naluSetsEqual("VPS", a.VPS, b.VPS); err != nil {
		return err
	}
	return nil
}

func naluSetsEqual(name string, a, b [][]byte) error {
	if len(a) != len(b) {
		return fmt.Errorf("%s count mismatch: %d vs %d", name, len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return fmt.Errorf("%s[%d] mismatch (%d bytes vs %d bytes)", name, i, len(a[i]), len(b[i]))
		}
	}
	return nil
}
