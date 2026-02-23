package validate

import (
	"fmt"
	"math/big"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/errors-go"
)

// FramesPerFragment defines how many frames are stored per fragment in a mez part.
type FramesPerFragment int

const (
	OneFramePerFragment FramesPerFragment = 1
)

// DefaultABRSegmentsPerPart is the default number of ABR segments per mez part.
const DefaultABRSegmentsPerPart = 15

// MezPartParams defines the expected properties of a mezzanine part for validation.
type MezPartParams struct {
	// FrameRate is the video frame rate as a rational number (e.g. 50/1 or 60000/1001).
	FrameRate *big.Rat

	// ABRVideoSegDuration is the duration of each ABR video segment in seconds
	// (e.g. 2.0 for 50fps, or 2.002 for 59.94fps).
	ABRVideoSegDuration *big.Rat

	// ABRAudioSegDuration is the duration of each ABR audio segment in seconds.
	ABRAudioSegDuration *big.Rat

	// ABRSegmentsPerPart is the number of ABR segments per mez part (default 15).
	ABRSegmentsPerPart int

	// FramesPerFrag specifies how many frames are stored per fragment.
	FramesPerFrag FramesPerFragment

	// StartPTS is the expected starting PTS of the part (typically 0).
	StartPTS uint64
}

// ContinuityProblem records a PTS/DTS continuity issue at a specific sample.
type ContinuityProblem struct {
	SampleIndex int
	Field       string // "PTS" or "DTS"
	Expected    uint64
	Actual      uint64
}

// MezPartResult holds the results of mez part validation.
type MezPartResult struct {
	// Errors collects all validation errors.
	Errors []string

	// ContinuityProblems records specific PTS/DTS continuity issues.
	ContinuityProblems []ContinuityProblem

	// MissingKeyFrames is the number of expected key frames that were not found.
	MissingKeyFrames int

	// FrameCount is the actual number of frames/samples found.
	FrameCount int

	// FragmentCount is the actual number of fragments found.
	FragmentCount int

	// Timescale is the media timescale extracted from the file.
	Timescale uint32

	// SampleDuration is the expected sample duration in timescale units.
	SampleDuration uint64
}

// ExpectedFrameCount returns the total number of frames expected in a mez part.
func (p *MezPartParams) ExpectedFrameCount() int {
	framesPerSeg := p.framesPerABRSegment()
	return framesPerSeg * p.ABRSegmentsPerPart
}

// ExpectedFragmentCount returns the total number of fragments expected.
func (p *MezPartParams) ExpectedFragmentCount() int {
	return p.ExpectedFrameCount() * int(p.FramesPerFrag)
}

// framesPerABRSegment returns the number of frames in one ABR segment.
// Computed as round(frame_rate * abr_segment_duration).
func (p *MezPartParams) framesPerABRSegment() int {
	// frames = frame_rate * segment_duration
	frames := new(big.Rat).Mul(p.FrameRate, p.ABRVideoSegDuration)
	// Round to nearest integer
	num := frames.Num()
	den := frames.Denom()
	// (num + den/2) / den
	half := new(big.Int).Div(den, big.NewInt(2))
	rounded := new(big.Int).Add(num, half)
	rounded.Div(rounded, den)
	return int(rounded.Int64())
}

// ValidateMezPart validates a mezzanine part file against expected parameters.
func ValidateMezPart(filename string, params *MezPartParams) (*MezPartResult, error) {
	e := errors.Template("validate.ValidateMezPart", "filename", filename)

	file, err := os.Open(filename)
	if err != nil {
		return nil, e(err)
	}
	defer file.Close()

	parsedMP4, err := mp4.DecodeFile(file)
	if err != nil {
		return nil, e(fmt.Errorf("failed to parse MP4: %w", err))
	}

	result := &MezPartResult{}

	// Extract timescale
	if parsedMP4.Moov == nil || len(parsedMP4.Moov.Traks) == 0 {
		return nil, e(fmt.Errorf("no moov/trak found"))
	}
	result.Timescale = parsedMP4.Moov.Traks[0].Mdia.Mdhd.Timescale

	// Compute expected sample duration in timescale units:
	// sample_duration = timescale / frame_rate
	// For frame_rate = num/den: sample_duration = timescale * den / num
	frNum := params.FrameRate.Num()
	frDen := params.FrameRate.Denom()
	result.SampleDuration = uint64(result.Timescale) * uint64(frDen.Int64()) / uint64(frNum.Int64())

	expectedFrames := params.ExpectedFrameCount()
	framesPerSeg := params.framesPerABRSegment()

	// Collect all samples across all fragments
	type sampleEntry struct {
		DTS         uint64
		PTS         uint64
		Duration    uint64
		IsSync      bool
		FragmentIdx int
	}

	var allSamples []sampleEntry
	seqPrev := uint32(0)
	fragGlobalIdx := 0

	for _, seg := range parsedMP4.Segments {
		for fragLocalIdx, frag := range seg.Fragments {
			result.FragmentCount++

			// Check sequence number continuity
			if frag.Moof != nil && frag.Moof.Mfhd != nil {
				seq := frag.Moof.Mfhd.SequenceNumber
				if fragGlobalIdx > 0 && seq != seqPrev+1 {
					result.Errors = append(result.Errors,
						fmt.Sprintf("fragment sequence gap: expected %d, got %d at fragment %d",
							seqPrev+1, seq, fragGlobalIdx))
				}
				seqPrev = seq
			}

			samples, sErr := frag.GetFullSamples(nil)
			if sErr != nil {
				result.Errors = append(result.Errors,
					fmt.Sprintf("failed to read samples at segment %d fragment %d: %v",
						0, fragLocalIdx, sErr))
				continue
			}

			for _, sample := range samples {
				allSamples = append(allSamples, sampleEntry{
					DTS:         sample.DecodeTime,
					PTS:         sample.PresentationTime(),
					Duration:    uint64(sample.Dur),
					IsSync:      sample.IsSync(),
					FragmentIdx: fragGlobalIdx,
				})
			}

			fragGlobalIdx++
		}
	}

	result.FrameCount = len(allSamples)

	// Check frame count
	if result.FrameCount != expectedFrames {
		result.Errors = append(result.Errors,
			fmt.Sprintf("frame count: expected %d, got %d", expectedFrames, result.FrameCount))
	}

	// Check fragment count (one frame per fragment)
	expectedFragments := expectedFrames / int(params.FramesPerFrag)
	if result.FragmentCount != expectedFragments {
		result.Errors = append(result.Errors,
			fmt.Sprintf("fragment count: expected %d, got %d", expectedFragments, result.FragmentCount))
	}

	// Validate DTS continuity: must be monotonic and exactly sample_duration apart
	for i, s := range allSamples {
		expectedDTS := params.StartPTS + uint64(i)*result.SampleDuration
		if s.DTS != expectedDTS {
			result.ContinuityProblems = append(result.ContinuityProblems, ContinuityProblem{
				SampleIndex: i,
				Field:       "DTS",
				Expected:    expectedDTS,
				Actual:      s.DTS,
			})
		}

		// Check sample duration
		if s.Duration != result.SampleDuration {
			result.Errors = append(result.Errors,
				fmt.Sprintf("sample %d: duration expected %d, got %d", i, result.SampleDuration, s.Duration))
		}
	}

	// Validate PTS continuity: sort by PTS and check monotonic + contiguous
	// Make a copy sorted by PTS for PTS gap checking
	ptsSorted := make([]sampleEntry, len(allSamples))
	copy(ptsSorted, allSamples)
	// Sort by PTS
	for i := 1; i < len(ptsSorted); i++ {
		for j := i; j > 0 && ptsSorted[j].PTS < ptsSorted[j-1].PTS; j-- {
			ptsSorted[j], ptsSorted[j-1] = ptsSorted[j-1], ptsSorted[j]
		}
	}

	for i, s := range ptsSorted {
		expectedPTS := params.StartPTS + uint64(i)*result.SampleDuration
		if s.PTS != expectedPTS {
			result.ContinuityProblems = append(result.ContinuityProblems, ContinuityProblem{
				SampleIndex: i,
				Field:       "PTS",
				Expected:    expectedPTS,
				Actual:      s.PTS,
			})
		}
	}

	// Check starting PTS
	if len(allSamples) > 0 {
		// Check first DTS
		if allSamples[0].DTS != params.StartPTS {
			result.Errors = append(result.Errors,
				fmt.Sprintf("first DTS: expected %d, got %d", params.StartPTS, allSamples[0].DTS))
		}
		// Check first PTS (from sorted)
		if ptsSorted[0].PTS != params.StartPTS {
			result.Errors = append(result.Errors,
				fmt.Sprintf("first PTS: expected %d, got %d", params.StartPTS, ptsSorted[0].PTS))
		}
	}

	// Check key frames: one every framesPerSeg frames (at ABR segment boundaries)
	for i, s := range allSamples {
		if i%framesPerSeg == 0 {
			if !s.IsSync {
				result.MissingKeyFrames++
				result.Errors = append(result.Errors,
					fmt.Sprintf("missing key frame at sample %d (DTS=%d)", i, s.DTS))
			}
		}
	}

	return result, nil
}

// Valid returns true if no errors were found.
func (r *MezPartResult) Valid() bool {
	return len(r.Errors) == 0 && len(r.ContinuityProblems) == 0 && r.MissingKeyFrames == 0
}

// AllErrors returns a combined error with all issues, or nil if valid.
func (r *MezPartResult) AllErrors() error {
	if r.Valid() {
		return nil
	}

	e := errors.Template("validate.MezPartResult",
		"frame_count", r.FrameCount,
		"fragment_count", r.FragmentCount,
		"missing_key_frames", r.MissingKeyFrames,
		"continuity_problems", len(r.ContinuityProblems))

	msg := fmt.Sprintf("%d errors, %d continuity problems, %d missing key frames",
		len(r.Errors), len(r.ContinuityProblems), r.MissingKeyFrames)

	for _, err := range r.Errors {
		msg += "\n  " + err
	}
	for i, cp := range r.ContinuityProblems {
		if i >= 10 {
			msg += fmt.Sprintf("\n  ... and %d more continuity problems", len(r.ContinuityProblems)-10)
			break
		}
		msg += fmt.Sprintf("\n  %s[%d]: expected %d, got %d", cp.Field, cp.SampleIndex, cp.Expected, cp.Actual)
	}

	return e(fmt.Errorf("%s", msg))
}
