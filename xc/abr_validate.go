package xc

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	mp4bits "github.com/Eyevinn/mp4ff/bits"
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

// ABRFrameTiming records decode/presentation timing for one video sample.
type ABRFrameTiming struct {
	Chunk       string
	DecodeIdx   int
	FrameType   string
	DTS         uint64
	PTS         int64
	Duration    uint32
	Composition int32
	Sync        bool
	NewSegment  bool
}

// SourceBFrameStats records source composition offset and decode/presentation displacement.
type SourceBFrameStats struct {
	MaxCompositionOffset          int32
	MaxCompositionOffsetFrame     int
	MaxCompositionOffsetFrameDur  uint32
	MaxPresentationDisplacement   int
	MaxPresentationDisplaceFrame  int
	MaxPresentationDisplacePTS    int64
	MaxPresentationDisplaceDTS    uint64
	MaxPresentationDisplaceTarget int
}

// ABRFrameTimingResult is returned by DASH chunk frame timing validation.
type ABRFrameTimingResult struct {
	Frames               []ABRFrameTiming
	ChunkCount           int
	FrameCount           int
	ContinuityMismatches []string
}

// SourceMP4FrameTimings parses fragmented MP4 source frame timing and B-frame reorder stats.
func SourceMP4FrameTimings(source string) ([]ABRFrameTiming, SourceBFrameStats, error) {
	f, err := os.Open(source)
	if err != nil {
		return nil, SourceBFrameStats{}, err
	}
	defer f.Close()

	parsed, err := mp4.DecodeFile(f)
	if err != nil {
		return nil, SourceBFrameStats{}, fmt.Errorf("failed to parse source MP4 %s: %w", source, err)
	}
	if parsed.Moov == nil {
		return nil, SourceBFrameStats{}, fmt.Errorf("source MP4 missing moov: %s", source)
	}

	var videoTrak *mp4.TrakBox
	for _, trak := range parsed.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Hdlr != nil && trak.Mdia.Hdlr.HandlerType == "vide" {
			videoTrak = trak
			break
		}
	}
	if videoTrak == nil {
		return nil, SourceBFrameStats{}, fmt.Errorf("source MP4 missing video track: %s", source)
	}
	if videoTrak.Mdia == nil || videoTrak.Mdia.Minf == nil || videoTrak.Mdia.Minf.Stbl == nil {
		return nil, SourceBFrameStats{}, fmt.Errorf("source video track missing sample table: %s", source)
	}
	stbl := videoTrak.Mdia.Minf.Stbl
	if stbl.Stts == nil {
		return nil, SourceBFrameStats{}, fmt.Errorf("source video track missing stts: %s", source)
	}
	if nrSamples := sttsSampleCount(stbl.Stts); nrSamples != 0 {
		return nil, SourceBFrameStats{}, fmt.Errorf("ABR bypass source should be a fragmented mez part: %s has %d stts samples", source, nrSamples)
	}

	frames, err := fragmentedMP4FrameTimings(parsed, source)
	if err != nil {
		return nil, SourceBFrameStats{}, err
	}
	return frames, CalculateSourceBFrameStats(frames), nil
}

// DashChunkFrameTimings parses DASH chunks and checks DTS/PTS continuity frame by frame.
func DashChunkFrameTimings(chunks []string) (*ABRFrameTimingResult, error) {
	result := &ABRFrameTimingResult{ChunkCount: len(chunks)}
	var (
		havePrevDtsEnd bool
		prevDtsEnd     uint64
		havePrevPtsEnd bool
		prevPtsEnd     int64
	)
	recordContinuityMismatch := func(format string, args ...interface{}) {
		if len(result.ContinuityMismatches) < 20 {
			result.ContinuityMismatches = append(result.ContinuityMismatches, fmt.Sprintf(format, args...))
		}
	}

	for _, chunk := range chunks {
		samples, err := DashChunkFullSamples(chunk)
		if err != nil {
			return result, err
		}
		if len(samples) == 0 {
			return result, fmt.Errorf("no video samples in %s", chunk)
		}

		if havePrevDtsEnd && prevDtsEnd != samples[0].DecodeTime {
			recordContinuityMismatch("DTS gap at start of %s expected %d got %d",
				filepath.Base(chunk), prevDtsEnd, samples[0].DecodeTime)
		}

		frames := make([]ABRFrameTiming, 0, len(samples))
		for i, sample := range samples {
			if sample.Dur == 0 {
				return result, fmt.Errorf("zero sample duration in %s decode frame %d", filepath.Base(chunk), i)
			}
			if i > 0 {
				expectedDTS := samples[i-1].DecodeTime + uint64(samples[i-1].Dur)
				if expectedDTS != sample.DecodeTime {
					recordContinuityMismatch("DTS gap in %s decode frame %d expected %d got %d",
						filepath.Base(chunk), i, expectedDTS, sample.DecodeTime)
				}
			}

			frame := ABRFrameTiming{
				Chunk:       chunk,
				DecodeIdx:   i,
				FrameType:   hevcFrameType(sample),
				DTS:         sample.DecodeTime,
				PTS:         sample.PresentationTime(),
				Duration:    sample.Dur,
				Composition: sample.CompositionTimeOffset,
				Sync:        sample.IsSync(),
				NewSegment:  i == 0,
			}
			frames = append(frames, frame)
			result.Frames = append(result.Frames, frame)
		}

		sort.SliceStable(frames, func(i, j int) bool {
			if frames[i].PTS == frames[j].PTS {
				return frames[i].DecodeIdx < frames[j].DecodeIdx
			}
			return frames[i].PTS < frames[j].PTS
		})

		for i, frame := range frames {
			if i == 0 {
				if havePrevPtsEnd && prevPtsEnd != frame.PTS {
					recordContinuityMismatch("PTS gap/overlap at start of %s expected %d got %d",
						filepath.Base(chunk), prevPtsEnd, frame.PTS)
				}
				continue
			}

			prev := frames[i-1]
			expectedPTS := prev.PTS + int64(prev.Duration)
			if expectedPTS != frame.PTS {
				recordContinuityMismatch("PTS gap/overlap in %s presentation frame %d expected %d got %d (decode frame %d follows decode frame %d)",
					filepath.Base(chunk), i, expectedPTS, frame.PTS, frame.DecodeIdx, prev.DecodeIdx)
			}
		}

		lastDecodeSample := samples[len(samples)-1]
		prevDtsEnd = lastDecodeSample.DecodeTime + uint64(lastDecodeSample.Dur)
		havePrevDtsEnd = true

		lastPresentationFrame := frames[len(frames)-1]
		prevPtsEnd = lastPresentationFrame.PTS + int64(lastPresentationFrame.Duration)
		havePrevPtsEnd = true
		result.FrameCount += len(samples)
	}

	return result, nil
}

// DashChunkFullSamples returns all samples in a DASH media segment.
func DashChunkFullSamples(chunk string) ([]mp4.FullSample, error) {
	f, err := os.Open(chunk)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	parsed, err := mp4.DecodeFile(f)
	if err != nil {
		return nil, fmt.Errorf("failed to parse DASH chunk %s: %w", chunk, err)
	}

	var samples []mp4.FullSample
	for _, segment := range parsed.Segments {
		for _, fragment := range segment.Fragments {
			fullSamples, err := fragment.GetFullSamples(nil)
			if err != nil {
				return nil, fmt.Errorf("failed to read samples from %s: %w", chunk, err)
			}
			samples = append(samples, fullSamples...)
		}
	}
	return samples, nil
}

func fragmentedMP4FrameTimings(parsed *mp4.File, source string) ([]ABRFrameTiming, error) {
	var frames []ABRFrameTiming
	for _, segment := range parsed.Segments {
		for _, fragment := range segment.Fragments {
			fullSamples, err := fragment.GetFullSamples(nil)
			if err != nil {
				return nil, fmt.Errorf("failed to read source fragment samples from %s: %w", source, err)
			}
			for _, sample := range fullSamples {
				frames = append(frames, ABRFrameTiming{
					DecodeIdx:   len(frames),
					FrameType:   hevcFrameType(sample),
					DTS:         sample.DecodeTime,
					PTS:         sample.PresentationTime(),
					Duration:    sample.Dur,
					Composition: sample.CompositionTimeOffset,
					Sync:        sample.IsSync(),
				})
			}
		}
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("source MP4 had no stts samples and no fragment samples: %s", source)
	}
	return frames, nil
}

// CalculateSourceBFrameStats calculates source B-frame composition and reorder stats.
func CalculateSourceBFrameStats(frames []ABRFrameTiming) SourceBFrameStats {
	var stats SourceBFrameStats
	for _, frame := range frames {
		if frame.Composition > stats.MaxCompositionOffset {
			stats.MaxCompositionOffset = frame.Composition
			stats.MaxCompositionOffsetFrame = frame.DecodeIdx
			stats.MaxCompositionOffsetFrameDur = frame.Duration
		}
	}

	presentationOrder := append([]ABRFrameTiming(nil), frames...)
	sort.SliceStable(presentationOrder, func(i, j int) bool {
		if presentationOrder[i].PTS == presentationOrder[j].PTS {
			return presentationOrder[i].DecodeIdx < presentationOrder[j].DecodeIdx
		}
		return presentationOrder[i].PTS < presentationOrder[j].PTS
	})
	for presentationIdx, frame := range presentationOrder {
		displacement := absInt(frame.DecodeIdx - presentationIdx)
		if displacement > stats.MaxPresentationDisplacement {
			stats.MaxPresentationDisplacement = displacement
			stats.MaxPresentationDisplaceFrame = frame.DecodeIdx
			stats.MaxPresentationDisplaceTarget = presentationIdx
			stats.MaxPresentationDisplacePTS = frame.PTS
			stats.MaxPresentationDisplaceDTS = frame.DTS
		}
	}

	return stats
}

// AssertSourceOutputFrameTimings returns an error if output DTS/PTS diverge from source timing.
func AssertSourceOutputFrameTimings(sourceFrames, outputFrames []ABRFrameTiming, startPts int64) error {
	if len(sourceFrames) == 0 {
		return fmt.Errorf("source frame timings empty")
	}
	if len(outputFrames) == 0 {
		return fmt.Errorf("output frame timings empty")
	}
	if len(sourceFrames) != len(outputFrames) {
		return fmt.Errorf("source/output frame count mismatch: %d vs %d", len(sourceFrames), len(outputFrames))
	}

	dtsOffset, ptsOffset := SourceOutputTimelineOffset(sourceFrames, outputFrames)
	if dtsOffset != ptsOffset {
		return fmt.Errorf("output DTS/PTS should have the same offset from source timeline: dts=%d pts=%d", dtsOffset, ptsOffset)
	}
	if dtsOffset != 0 && dtsOffset != startPts {
		return fmt.Errorf("output timeline offset should be either chunk-local (0) or start_pts (%d), got %d", startPts, dtsOffset)
	}

	var (
		mismatchCount int
		mismatches    []string
	)
	for i := range sourceFrames {
		expectedDTS := int64(sourceFrames[i].DTS) + dtsOffset
		expectedPTS := sourceFrames[i].PTS + ptsOffset
		gotDTS := int64(outputFrames[i].DTS)
		gotPTS := outputFrames[i].PTS
		if expectedDTS == gotDTS && expectedPTS == gotPTS {
			continue
		}
		mismatchCount++
		if len(mismatches) < 20 {
			mismatches = append(mismatches,
				fmt.Sprintf("frame %d expected %d/%d got %d/%d", i, expectedDTS, expectedPTS, gotDTS, gotPTS))
		}
	}
	if mismatchCount > 0 {
		return fmt.Errorf("source/output DTS/PTS mismatches with start_pts=%d output_offset=%d/%d: %d\n%s",
			startPts, dtsOffset, ptsOffset, mismatchCount, strings.Join(mismatches, "\n"))
	}
	return nil
}

// SourceOutputPTSComparison returns a source/output DTS/PTS comparison table.
func SourceOutputPTSComparison(sourceFrames, outputFrames []ABRFrameTiming, stats SourceBFrameStats, startPts int64) string {
	var b strings.Builder
	dtsOffset, ptsOffset := SourceOutputTimelineOffset(sourceFrames, outputFrames)

	fmt.Fprintf(&b, "\nsource/output DTS/PTS comparison\n")
	fmt.Fprintf(&b, "requested start_pts: %d\n", startPts)
	fmt.Fprintf(&b, "observed output offset from source: dts=%d pts=%d\n", dtsOffset, ptsOffset)
	fmt.Fprintf(&b, "content timeline column: source + start_pts\n")
	fmt.Fprintf(&b, "source max B-frame composition offset: %d ticks", stats.MaxCompositionOffset)
	if stats.MaxCompositionOffsetFrameDur > 0 {
		offsetFrames := float64(stats.MaxCompositionOffset) / float64(stats.MaxCompositionOffsetFrameDur)
		fmt.Fprintf(&b, " (%.2f frames)", offsetFrames)
	}
	fmt.Fprintf(&b, " at source decode_frame=%d\n", stats.MaxCompositionOffsetFrame)
	fmt.Fprintf(&b, "source max decode/presentation displacement: %d frames at source decode_frame=%d presentation_frame=%d dts=%d pts=%d\n",
		stats.MaxPresentationDisplacement,
		stats.MaxPresentationDisplaceFrame,
		stats.MaxPresentationDisplaceTarget,
		stats.MaxPresentationDisplaceDTS,
		stats.MaxPresentationDisplacePTS)
	fmt.Fprintf(&b, "%6s  %4s  %18s  %18s  %18s  %-7s  %s\n",
		"frame", "type", "source dts/pts", "content dts/pts", "output dts/pts", "output", "diff")

	rowCount := len(sourceFrames)
	if len(outputFrames) > rowCount {
		rowCount = len(outputFrames)
	}
	for i := 0; i < rowCount; i++ {
		sourceTiming := "-"
		expectedTiming := "-"
		outputTiming := "-"
		frameType := "?"
		outputMarker := ""
		diff := "-"

		switch {
		case i >= len(sourceFrames):
			output := outputFrames[i]
			outputTiming = fmt.Sprintf("%d/%d", output.DTS, output.PTS)
			frameType = output.FrameType
			if output.NewSegment {
				outputMarker = "new seg"
			}
			diff = "missing_source"
		case i >= len(outputFrames):
			source := sourceFrames[i]
			sourceTiming = fmt.Sprintf("%d/%d", source.DTS, source.PTS)
			expectedTiming = fmt.Sprintf("%d/%d", int64(source.DTS)+startPts, source.PTS+startPts)
			frameType = source.FrameType
			diff = "missing_output"
		default:
			source := sourceFrames[i]
			output := outputFrames[i]
			sourceTiming = fmt.Sprintf("%d/%d", source.DTS, source.PTS)
			contentDTS := int64(source.DTS) + startPts
			contentPTS := source.PTS + startPts
			expectedDTS := int64(source.DTS) + dtsOffset
			expectedPTS := source.PTS + ptsOffset
			expectedTiming = fmt.Sprintf("%d/%d", contentDTS, contentPTS)
			outputTiming = fmt.Sprintf("%d/%d", output.DTS, output.PTS)
			frameType = source.FrameType
			if frameType == "?" {
				frameType = output.FrameType
			}
			if output.NewSegment {
				outputMarker = "new seg"
			}
			var differences []string
			if expectedDTS != int64(output.DTS) {
				differences = append(differences, "DTS")
			}
			if expectedPTS != output.PTS {
				differences = append(differences, "PTS")
			}
			if len(differences) > 0 {
				diff = strings.Join(differences, ",")
			}
		}

		fmt.Fprintf(&b, "%6d  %4s  %18s  %18s  %18s  %-7s  %s\n",
			i, frameType, sourceTiming, expectedTiming, outputTiming, outputMarker, diff)
	}

	return b.String()
}

// SourceOutputTimelineOffset returns output timeline offset from the source timeline.
func SourceOutputTimelineOffset(sourceFrames, outputFrames []ABRFrameTiming) (int64, int64) {
	if len(sourceFrames) == 0 || len(outputFrames) == 0 {
		return 0, 0
	}
	return int64(outputFrames[0].DTS) - int64(sourceFrames[0].DTS),
		outputFrames[0].PTS - sourceFrames[0].PTS
}

type abrContentFrame struct {
	chunk     string
	decodeIdx int
	frameType string
	dts       int64
	pts       int64
	duration  uint32
}

// AssertABRBypassPartsContiguous verifies the last segment of one part is contiguous with the first segment of the next.
func AssertABRBypassPartsContiguous(previousName string, previousStartPts int64, previousSourceFrames, previousOutputFrames []ABRFrameTiming, currentName string, currentStartPts int64, currentSourceFrames, currentOutputFrames []ABRFrameTiming) error {
	if len(previousOutputFrames) == 0 {
		return fmt.Errorf("%s output frames empty", previousName)
	}
	if len(currentOutputFrames) == 0 {
		return fmt.Errorf("%s output frames empty", currentName)
	}

	previousLastChunk := previousOutputFrames[len(previousOutputFrames)-1].Chunk
	currentFirstChunk := currentOutputFrames[0].Chunk
	previousSegmentFrames, err := abrBypassContentSegmentFrames(previousStartPts, previousSourceFrames, previousOutputFrames, previousLastChunk)
	if err != nil {
		return fmt.Errorf("%s last segment %s: %w", previousName, filepath.Base(previousLastChunk), err)
	}
	currentSegmentFrames, err := abrBypassContentSegmentFrames(currentStartPts, currentSourceFrames, currentOutputFrames, currentFirstChunk)
	if err != nil {
		return fmt.Errorf("%s first segment %s: %w", currentName, filepath.Base(currentFirstChunk), err)
	}

	previousLastDecode := previousSegmentFrames[len(previousSegmentFrames)-1]
	currentFirstDecode := currentSegmentFrames[0]
	previousDecodeEnd := previousLastDecode.dts + int64(previousLastDecode.duration)
	if previousDecodeEnd != currentFirstDecode.dts {
		return fmt.Errorf("%s last segment %s -> %s first segment %s DTS continuity: previous dts/dur=%d/%d next dts=%d",
			previousName, filepath.Base(previousLastChunk),
			currentName, filepath.Base(currentFirstChunk),
			previousLastDecode.dts, previousLastDecode.duration, currentFirstDecode.dts)
	}

	previousPresentationFrames := abrBypassPresentationOrder(previousSegmentFrames)
	currentPresentationFrames := abrBypassPresentationOrder(currentSegmentFrames)
	previousLastPresentation := previousPresentationFrames[len(previousPresentationFrames)-1]
	currentFirstPresentation := currentPresentationFrames[0]
	previousPresentationEnd := previousLastPresentation.pts + int64(previousLastPresentation.duration)
	if previousPresentationEnd != currentFirstPresentation.pts {
		return fmt.Errorf("%s last segment %s -> %s first segment %s PTS continuity: previous pts/dur=%d/%d next pts=%d",
			previousName, filepath.Base(previousLastChunk),
			currentName, filepath.Base(currentFirstChunk),
			previousLastPresentation.pts, previousLastPresentation.duration, currentFirstPresentation.pts)
	}
	return nil
}

func abrBypassContentSegmentFrames(startPts int64, sourceFrames, outputFrames []ABRFrameTiming, chunk string) ([]abrContentFrame, error) {
	dtsOffset, ptsOffset := SourceOutputTimelineOffset(sourceFrames, outputFrames)
	frames := make([]abrContentFrame, 0)
	for _, frame := range outputFrames {
		if frame.Chunk != chunk {
			continue
		}
		frames = append(frames, abrContentFrame{
			chunk:     frame.Chunk,
			decodeIdx: frame.DecodeIdx,
			frameType: frame.FrameType,
			dts:       int64(frame.DTS) - dtsOffset + startPts,
			pts:       frame.PTS - ptsOffset + startPts,
			duration:  frame.Duration,
		})
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("missing output frames for chunk %s", filepath.Base(chunk))
	}
	return frames, nil
}

func abrBypassPresentationOrder(frames []abrContentFrame) []abrContentFrame {
	presentationOrder := append([]abrContentFrame(nil), frames...)
	sort.SliceStable(presentationOrder, func(i, j int) bool {
		if presentationOrder[i].pts == presentationOrder[j].pts {
			return presentationOrder[i].decodeIdx < presentationOrder[j].decodeIdx
		}
		return presentationOrder[i].pts < presentationOrder[j].pts
	})
	return presentationOrder
}

type dashMPD struct {
	Periods []dashPeriod `xml:"Period"`
}

type dashPeriod struct {
	AdaptationSets []dashAdaptationSet `xml:"AdaptationSet"`
}

type dashAdaptationSet struct {
	Representations []dashRepresentation `xml:"Representation"`
}

type dashRepresentation struct {
	SegmentTemplate dashSegmentTemplate `xml:"SegmentTemplate"`
}

type dashSegmentTemplate struct {
	SegmentTimeline dashSegmentTimeline `xml:"SegmentTimeline"`
}

type dashSegmentTimeline struct {
	Entries []dashSegmentTimelineEntry `xml:"S"`
}

type dashSegmentTimelineEntry struct {
	T int64 `xml:"t,attr"`
}

// AssertDashMPDSegmentTimelineStart checks the DASH MPD starts on the expected source or content timeline.
func AssertDashMPDSegmentTimelineStart(dashDir string, sourceFrames []ABRFrameTiming, startPts int64) error {
	if len(sourceFrames) == 0 {
		return fmt.Errorf("source frame timings empty")
	}
	mpdPath := filepath.Join(dashDir, "dash.mpd")
	f, err := os.Open(mpdPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var mpd dashMPD
	if err := xml.NewDecoder(f).Decode(&mpd); err != nil {
		return fmt.Errorf("failed to parse %s: %w", mpdPath, err)
	}
	if len(mpd.Periods) == 0 {
		return fmt.Errorf("%s missing Period", mpdPath)
	}
	if len(mpd.Periods[0].AdaptationSets) == 0 {
		return fmt.Errorf("%s missing AdaptationSet", mpdPath)
	}
	if len(mpd.Periods[0].AdaptationSets[0].Representations) == 0 {
		return fmt.Errorf("%s missing Representation", mpdPath)
	}
	entries := mpd.Periods[0].AdaptationSets[0].Representations[0].SegmentTemplate.SegmentTimeline.Entries
	if len(entries) == 0 {
		return fmt.Errorf("%s missing SegmentTimeline entries", mpdPath)
	}

	expectedStart := sourceFrames[0].PTS + startPts
	localStart := sourceFrames[0].PTS
	if entries[0].T != localStart && entries[0].T != expectedStart {
		return fmt.Errorf("MPD SegmentTimeline start should be either local (%d) or source+start_pts (%d), got %d",
			localStart, expectedStart, entries[0].T)
	}
	return nil
}

func hevcFrameType(sample mp4.FullSample) string {
	frameType := hevcFrameTypeFromSample(sample.Data)
	if frameType != "?" {
		return frameType
	}
	if sample.IsSync() {
		return "I"
	}
	return "?"
}

func hevcFrameTypeFromSample(data []byte) string {
	nalusByLayer := hevc.SplitNalusByLayerID(data, 4)
	if len(nalusByLayer) == 0 {
		return "?"
	}

	if frameType := hevcFrameTypeFromNalus(nalusByLayer[0]); frameType != "?" {
		return frameType
	}

	layerIDs := make([]int, 0, len(nalusByLayer))
	for layerID := range nalusByLayer {
		if layerID != 0 {
			layerIDs = append(layerIDs, int(layerID))
		}
	}
	sort.Ints(layerIDs)
	for _, layerID := range layerIDs {
		if frameType := hevcFrameTypeFromNalus(nalusByLayer[byte(layerID)]); frameType != "?" {
			return frameType
		}
	}
	return "?"
}

func hevcFrameTypeFromNalus(nalus [][]byte) string {
	for _, nalu := range nalus {
		if len(nalu) < 2 {
			continue
		}
		naluInfo := hevc.ParseNaluHeader(nalu[:2])
		if !hevc.IsVideoNaluType(naluInfo.Type) {
			continue
		}
		if naluInfo.Type >= hevc.NALU_BLA_W_LP && naluInfo.Type <= hevc.NALU_IRAP_VCL23 {
			return "I"
		}

		sliceType, ok := hevcSliceType(nalu, naluInfo.Type)
		if !ok {
			continue
		}
		switch sliceType {
		case hevc.SLICE_B:
			return "B"
		case hevc.SLICE_P:
			return "P"
		case hevc.SLICE_I:
			return "I"
		default:
			return "?"
		}
	}
	return "?"
}

func hevcSliceType(nalu []byte, naluType hevc.NaluType) (hevc.SliceType, bool) {
	br := mp4bits.NewEBSPReader(bytes.NewReader(nalu))

	br.Read(16)
	firstSliceSegmentInPicFlag := br.ReadFlag()

	if naluType >= hevc.NALU_BLA_W_LP && naluType <= hevc.NALU_IRAP_VCL23 {
		br.ReadFlag()
	}

	br.ReadExpGolomb()
	if br.AccError() != nil || !firstSliceSegmentInPicFlag {
		return 0, false
	}

	sliceType := hevc.SliceType(br.ReadExpGolomb())
	if br.AccError() != nil {
		return 0, false
	}
	return sliceType, true
}

func sttsSampleCount(stts *mp4.SttsBox) uint32 {
	var total uint32
	for _, sampleCount := range stts.SampleCount {
		total += sampleCount
	}
	return total
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
