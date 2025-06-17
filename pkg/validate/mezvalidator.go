package validate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"
)

// MezValidationResult holds the results of MP4 validation
type MezValidationResult struct {
	Valid                    bool
	SampleCount              int
	UniformSampleDuration    bool
	ExpectedSampleDuration   uint32
	ActualSampleDurations    []uint32
	MFHDSequenceNumbers      []uint32
	TFDTBaseTimes            []uint64
	ExpectedBaseTime         uint64
	TotalDuration            uint64
	Timescale                uint32
	FileSize                 int64
	Issues                   []string
}

// ValidateMez validates an fMP4 mezzanine segment file using mp4ff library
func ValidateMez(filename string) (*MezValidationResult, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %v", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			fmt.Printf("Warning: failed to close file: %v\n", closeErr)
		}
	}()

	// Get file size
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to get file info: %v", err)
	}
	fileSize := fileInfo.Size()

	// Parse the MP4 file using mp4ff
	parsedMP4, err := mp4.DecodeFile(file)
	if err != nil {
		return nil, fmt.Errorf("failed to parse MP4 file: %v", err)
	}

	result := &MezValidationResult{
		Valid:                 true,
		Issues:                []string{},
		ActualSampleDurations: []uint32{},
		MFHDSequenceNumbers:   []uint32{},
		TFDTBaseTimes:         []uint64{},
		FileSize:              fileSize,
	}

	// Extract timescale from moov if present
	if parsedMP4.Moov != nil && len(parsedMP4.Moov.Traks) > 0 {
		result.Timescale = parsedMP4.Moov.Traks[0].Mdia.Mdhd.Timescale
	}

	// Analyze fragments
	err = analyzeFragments(parsedMP4, result)
	if err != nil {
		return nil, fmt.Errorf("failed to analyze fragments: %v", err)
	}

	// Validate the extracted information
	validateResults(result)

	return result, nil
}

// analyzeFragments extracts information from movie fragments
func analyzeFragments(parsedMP4 *mp4.File, result *MezValidationResult) error {
	// Get default sample duration from TREX box in MOOV
	var trexDefaultDuration uint32 = 0
	if parsedMP4.Moov != nil && parsedMP4.Moov.Mvex != nil && len(parsedMP4.Moov.Mvex.Trexs) > 0 {
		trexDefaultDuration = parsedMP4.Moov.Mvex.Trexs[0].DefaultSampleDuration
	}

	for _, segment := range parsedMP4.Segments {
		for _, fragment := range segment.Fragments {
			// Extract MFHD sequence number
			if fragment.Moof.Mfhd != nil {
				result.MFHDSequenceNumbers = append(result.MFHDSequenceNumbers, fragment.Moof.Mfhd.SequenceNumber)
			}

			// Analyze track fragments
			for _, traf := range fragment.Moof.Trafs {
				// Extract TFDT base time
				if traf.Tfdt != nil {
					result.TFDTBaseTimes = append(result.TFDTBaseTimes, traf.Tfdt.BaseMediaDecodeTime())
				}

				// Get default sample duration from TFHD box (overrides TREX)
				tfhdDefaultDuration := trexDefaultDuration
				if traf.Tfhd != nil && (traf.Tfhd.Flags&0x000008) != 0 { // default-sample-duration-present
					tfhdDefaultDuration = traf.Tfhd.DefaultSampleDuration
				}

				// Extract sample durations from TRUN boxes
				for _, trun := range traf.Truns {
					result.SampleCount += int(trun.SampleCount())
					
					// Check if sample durations are present in the trun
					if (trun.Flags & 0x100) != 0 { // sample duration present flag
						for i := uint32(0); i < trun.SampleCount(); i++ {
							if i < uint32(len(trun.Samples)) {
								result.ActualSampleDurations = append(result.ActualSampleDurations, trun.Samples[i].Dur)
							}
						}
					} else {
						// Use default duration from TFHD or TREX
						for i := uint32(0); i < trun.SampleCount(); i++ {
							result.ActualSampleDurations = append(result.ActualSampleDurations, tfhdDefaultDuration)
						}
					}
				}
			}
		}
	}
	return nil
}

// validateResults performs the actual validation logic
func validateResults(result *MezValidationResult) {
	// Check if all sample durations are the same
	if len(result.ActualSampleDurations) > 0 {
		result.ExpectedSampleDuration = result.ActualSampleDurations[0]
		result.UniformSampleDuration = true
		
		for i, duration := range result.ActualSampleDurations {
			if duration != result.ExpectedSampleDuration {
				result.UniformSampleDuration = false
				result.Valid = false
				result.Issues = append(result.Issues, 
					fmt.Sprintf("Sample %d has duration %d, expected %d", 
						i, duration, result.ExpectedSampleDuration))
			}
		}
		
		// Calculate total duration
		for _, duration := range result.ActualSampleDurations {
			result.TotalDuration += uint64(duration)
		}
	}
	
	// Check MFHD sequence numbers start from 1 and increment by 1
	for i, seqNum := range result.MFHDSequenceNumbers {
		expected := uint32(i + 1)
		if seqNum != expected {
			result.Valid = false
			result.Issues = append(result.Issues, 
				fmt.Sprintf("MFHD sequence number at fragment %d is %d, expected %d", 
					i, seqNum, expected))
		}
	}
	
	// Check TFDT base times are consistent with cumulative sample durations
	var cumulativeDuration uint64 = 0
	for i, baseTime := range result.TFDTBaseTimes {
		if i == 0 {
			// First fragment should start at base time zero
			if baseTime != 0 {
				result.Valid = false
				result.Issues = append(result.Issues, 
					fmt.Sprintf("First TFDT base time is %d, expected 0", baseTime))
			}
			cumulativeDuration = baseTime
		} else {
			expectedBaseTime := cumulativeDuration
			if baseTime != expectedBaseTime {
				result.Valid = false
				result.Issues = append(result.Issues, 
					fmt.Sprintf("TFDT base time at fragment %d is %d, expected %d (cumulative duration)", 
						i, baseTime, expectedBaseTime))
			}
		}
		
		// Add this fragment's duration to cumulative total
		// This assumes all samples in a fragment belong to the same track
		if result.UniformSampleDuration && len(result.ActualSampleDurations) > 0 {
			// Calculate samples in this fragment (simplified assumption)
			samplesPerFragment := len(result.ActualSampleDurations) / len(result.TFDTBaseTimes)
			if samplesPerFragment > 0 {
				fragmentDuration := uint64(samplesPerFragment) * uint64(result.ExpectedSampleDuration)
				cumulativeDuration += fragmentDuration
			}
		}
	}
	
	result.ExpectedBaseTime = cumulativeDuration
}

// MezValidationReport represents the JSON validation report
type MezValidationReport struct {
	Pass                     bool     `json:"pass"`
	Filename                 string   `json:"filename"`
	SampleCount              int      `json:"sample_count"`
	CommonSampleDuration     uint32   `json:"common_sample_duration"`
	CommonDurationSeconds    float64  `json:"common_duration_seconds,omitempty"`
	Timescale                uint32   `json:"timescale,omitempty"`
	TotalDuration            uint64   `json:"total_duration"`
	TotalDurationSeconds     float64  `json:"total_duration_seconds,omitempty"`
	FileSize                 int64    `json:"file_size,omitempty"`
	AverageBitrateBps        int64    `json:"average_bitrate_bps,omitempty"`
	MFHDSequenceNumbers      []uint32 `json:"mfhd_sequence_numbers,omitempty"`
	TFDTBaseTimes            []uint64 `json:"tfdt_base_times,omitempty"`
	DurationVariations       []DurationVariation `json:"duration_variations,omitempty"`
	SequenceNumberErrors     []SequenceError     `json:"sequence_number_errors,omitempty"`
	TFDTTimeErrors           []TFDTError         `json:"tfdt_time_errors,omitempty"`
}

type DurationVariation struct {
	SampleIndex    int    `json:"sample_index"`
	ActualDuration uint32 `json:"actual_duration"`
}

type SequenceError struct {
	FragmentIndex    int    `json:"fragment_index"`
	ActualSequence   uint32 `json:"actual_sequence"`
	ExpectedSequence uint32 `json:"expected_sequence"`
}

type TFDTError struct {
	FragmentIndex    int    `json:"fragment_index"`
	ActualBaseTime   uint64 `json:"actual_base_time"`
	ExpectedBaseTime uint64 `json:"expected_base_time"`
}

// PrintMezValidationResult prints a concise JSON validation report to the specified writer
func PrintMezValidationResult(w io.Writer, filename string, result *MezValidationResult) {
	report := createValidationReport(filename, result)
	
	jsonBytes, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		_, _ = fmt.Fprintf(w, "Error creating JSON report: %v\n", err)
		return
	}
	
	_, _ = fmt.Fprintln(w, string(jsonBytes))
}

// createValidationReport creates a MezValidationReport from MezValidationResult
func createValidationReport(filename string, result *MezValidationResult) *MezValidationReport {
	report := &MezValidationReport{
		Pass:        result.Valid,
		Filename:    filename,
		SampleCount: result.SampleCount,
		FileSize:    result.FileSize,
	}
	
	// Set common duration and timescale info
	if len(result.ActualSampleDurations) > 0 {
		report.CommonSampleDuration = result.ExpectedSampleDuration
		report.TotalDuration = result.TotalDuration
		if result.Timescale > 0 {
			report.Timescale = result.Timescale
			report.CommonDurationSeconds = float64(report.CommonSampleDuration) / float64(result.Timescale)
			report.TotalDurationSeconds = float64(report.TotalDuration) / float64(result.Timescale)
			
			// Calculate average bitrate if we have duration and file size
			if report.TotalDurationSeconds > 0 && result.FileSize > 0 {
				// Average bitrate = (file_size_bytes * 8) / duration_seconds
				report.AverageBitrateBps = int64(float64(result.FileSize*8) / report.TotalDurationSeconds)
			}
		}
	}
	
	// Only include variations from the common duration
	for i, duration := range result.ActualSampleDurations {
		if duration != result.ExpectedSampleDuration {
			report.DurationVariations = append(report.DurationVariations, DurationVariation{
				SampleIndex:    i,
				ActualDuration: duration,
			})
		}
	}
	
	// Check if sequence numbers are regular (1, 2, 3, ...)
	sequenceIsRegular := true
	for i, seqNum := range result.MFHDSequenceNumbers {
		expected := uint32(i + 1)
		if seqNum != expected {
			sequenceIsRegular = false
			report.SequenceNumberErrors = append(report.SequenceNumberErrors, SequenceError{
				FragmentIndex:    i,
				ActualSequence:   seqNum,
				ExpectedSequence: expected,
			})
		}
	}
	
	// Only include sequence numbers if they're not regular
	if !sequenceIsRegular {
		report.MFHDSequenceNumbers = result.MFHDSequenceNumbers
	}
	
	// Check if TFDT times are regular (following cumulative sample duration)
	var cumulativeDuration uint64 = 0
	tfdtIsRegular := true
	for i, baseTime := range result.TFDTBaseTimes {
		if i == 0 {
			// First fragment should start at base time zero
			if baseTime != 0 {
				tfdtIsRegular = false
				report.TFDTTimeErrors = append(report.TFDTTimeErrors, TFDTError{
					FragmentIndex:    i,
					ActualBaseTime:   baseTime,
					ExpectedBaseTime: 0,
				})
			}
			cumulativeDuration = baseTime
		} else {
			expectedBaseTime := cumulativeDuration
			if baseTime != expectedBaseTime {
				tfdtIsRegular = false
				report.TFDTTimeErrors = append(report.TFDTTimeErrors, TFDTError{
					FragmentIndex:    i,
					ActualBaseTime:   baseTime,
					ExpectedBaseTime: expectedBaseTime,
				})
			}
		}
		
		// Update cumulative duration for next fragment
		if result.UniformSampleDuration && len(result.ActualSampleDurations) > 0 {
			samplesPerFragment := len(result.ActualSampleDurations) / len(result.TFDTBaseTimes)
			if samplesPerFragment > 0 {
				fragmentDuration := uint64(samplesPerFragment) * uint64(result.ExpectedSampleDuration)
				cumulativeDuration += fragmentDuration
			}
		}
	}
	
	// Only include TFDT times if they're not regular
	if !tfdtIsRegular {
		report.TFDTBaseTimes = result.TFDTBaseTimes
	}
	
	return report
}

// ValidateMezFile is a convenience function that validates a file and prints results to stdout
func ValidateMezFile(filename string) error {
	result, err := ValidateMez(filename)
	if err != nil {
		return err
	}
	
	PrintMezValidationResult(os.Stdout, filename, result)
	return nil
}