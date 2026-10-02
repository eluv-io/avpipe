package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/pkg/validate"
	"github.com/stretchr/testify/require"
)

func TestValidateVideoMezSegment1(t *testing.T) {
	// Path to the test file
	testFile := filepath.Join("testdata", "video-mez-segment-1.mp4")

	// Run the validation
	minBitrateKbps := 100 // Minimum bitrate in kbps
	maxBitrateKbps := 200 // Maximum bitrate in kbps
	result, err := validate.ValidateMez(testFile, minBitrateKbps, maxBitrateKbps)
	if err != nil {
		t.Fatalf("ValidateMez failed: %v", err)
	}

	// Capture the JSON output
	var buf bytes.Buffer
	validate.PrintMezValidationResult(&buf, testFile, result)

	// Parse the actual output
	var actualReport validate.MezValidationReport
	err = json.Unmarshal(buf.Bytes(), &actualReport)
	if err != nil {
		t.Fatalf("Failed to parse JSON output: %v", err)
	}

	// Expected output based on the known characteristics of video-mez-segment-1.mp4
	// No errors expected, uniform sample durations, and correct segment structure
	expectedReport := validate.MezValidationReport{
		Pass:                  true,
		Filename:              testFile,
		SampleCount:           840,
		CommonSampleDuration:  3600,
		CommonDurationSeconds: 0.04,
		Timescale:             90000,
		TotalDuration:         3024000,
		TotalDurationSeconds:  33.6,
		FileSize:              506271,
		AverageBitrateKBps:    121,
	}

	require.Equal(t, expectedReport, actualReport, "Expected and actual reports do not match")
}

// createModifiedMP4 creates a modified version of the input MP4 file with altered sample duration
// in the 5th fragment's TFHD box and writes it to a temporary file
func createModifiedMP4(inputPath string, newSampleDuration uint32) (string, error) {
	// Open and parse the original file
	file, err := os.Open(inputPath)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
	}()

	parsedMP4, err := mp4.DecodeFile(file)
	if err != nil {
		return "", err
	}

	// Modify the 5th fragment's TFHD DefaultSampleDuration
	if len(parsedMP4.Segments) > 0 && len(parsedMP4.Segments[0].Fragments) >= 5 {
		fragment := parsedMP4.Segments[0].Fragments[4] // 5th fragment (0-indexed)
		if len(fragment.Moof.Trafs) > 0 && fragment.Moof.Trafs[0].Tfhd != nil {
			// Set the default-sample-duration-present flag and modify the duration
			fragment.Moof.Trafs[0].Tfhd.Flags |= 0x000008
			fragment.Moof.Trafs[0].Tfhd.DefaultSampleDuration = newSampleDuration
		}
	}

	// Create temporary file
	tmpFile, err := os.CreateTemp("", "modified_mez_*.mp4")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = tmpFile.Close()
	}()

	// Write the modified MP4 to temporary file
	err = parsedMP4.Encode(tmpFile)
	if err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", err
	}

	return tmpFile.Name(), nil
}

func TestValidateModifiedVideoMezSegment(t *testing.T) {
	// Path to the original test file
	originalFile := filepath.Join("testdata", "video-mez-segment-1.mp4")

	// Create modified version with different sample duration in 5th fragment
	modifiedFile, err := createModifiedMP4(originalFile, 3605)
	if err != nil {
		t.Fatalf("Failed to create modified MP4: %v", err)
	}
	defer func() {
		_ = os.Remove(modifiedFile) // Clean up
	}()

	// Run validation on the modified file
	minBitrateKbps := 100 // Minimum bitrate in kbps
	maxBitrateKbps := 200 // Maximum bitrate in kbps
	result, err := validate.ValidateMez(modifiedFile, minBitrateKbps, maxBitrateKbps)
	if err != nil {
		t.Fatalf("ValidateMez failed: %v", err)
	}

	// Capture the JSON output
	var buf bytes.Buffer
	validate.PrintMezValidationResult(&buf, modifiedFile, result)

	// Parse the actual output
	var actualReport validate.MezValidationReport
	err = json.Unmarshal(buf.Bytes(), &actualReport)
	if err != nil {
		t.Fatalf("Failed to parse JSON output: %v", err)
	}

	// The file should now fail validation due to non-uniform sample durations
	if actualReport.Pass {
		t.Error("Expected validation to fail due to modified sample duration, but it passed")
	}

	// Should have duration variations
	if len(actualReport.DurationVariations) != 1 {
		t.Error("Expected one duration variation to be reported, but got", len(actualReport.DurationVariations))
	}

	// Verify that the duration variation includes the modified samples
	foundModifiedDuration := false
	for _, variation := range actualReport.DurationVariations {
		if variation.ActualDuration == 3605 {
			foundModifiedDuration = true
			break
		}
	}
	if !foundModifiedDuration {
		t.Error("Expected to find duration variation with value 3605, but it was not reported")
	}
}
