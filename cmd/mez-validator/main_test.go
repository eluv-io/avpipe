package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/pkg/validate"
)

func TestValidateVideoMezSegment1(t *testing.T) {
	// Path to the test file
	testFile := filepath.Join("testdata", "video-mez-segment-1.mp4")

	// Run the validation
	result, err := validate.ValidateMez(testFile)
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
	expectedReport := validate.MezValidationReport{
		Pass:                  true,
		SampleCount:           840,
		CommonSampleDuration:  3600,
		CommonDurationSeconds: 0.04,
		Timescale:             90000,
		TotalDuration:         3024000,
		TotalDurationSeconds:  33.6,
		FileSize:              506271,
		AverageBitrateBps:     120540,
	}

	// Compare the results
	if actualReport.Pass != expectedReport.Pass {
		t.Errorf("Pass mismatch: got %v, want %v", actualReport.Pass, expectedReport.Pass)
	}

	if actualReport.SampleCount != expectedReport.SampleCount {
		t.Errorf("SampleCount mismatch: got %d, want %d", actualReport.SampleCount, expectedReport.SampleCount)
	}

	if actualReport.CommonSampleDuration != expectedReport.CommonSampleDuration {
		t.Errorf("CommonSampleDuration mismatch: got %d, want %d", actualReport.CommonSampleDuration, expectedReport.CommonSampleDuration)
	}

	if actualReport.CommonDurationSeconds != expectedReport.CommonDurationSeconds {
		t.Errorf("CommonDurationSeconds mismatch: got %f, want %f", actualReport.CommonDurationSeconds, expectedReport.CommonDurationSeconds)
	}

	if actualReport.Timescale != expectedReport.Timescale {
		t.Errorf("Timescale mismatch: got %d, want %d", actualReport.Timescale, expectedReport.Timescale)
	}

	if actualReport.TotalDuration != expectedReport.TotalDuration {
		t.Errorf("TotalDuration mismatch: got %d, want %d", actualReport.TotalDuration, expectedReport.TotalDuration)
	}

	if actualReport.TotalDurationSeconds != expectedReport.TotalDurationSeconds {
		t.Errorf("TotalDurationSeconds mismatch: got %f, want %f", actualReport.TotalDurationSeconds, expectedReport.TotalDurationSeconds)
	}

	if actualReport.FileSize != expectedReport.FileSize {
		t.Errorf("FileSize mismatch: got %d, want %d", actualReport.FileSize, expectedReport.FileSize)
	}

	if actualReport.AverageBitrateBps != expectedReport.AverageBitrateBps {
		t.Errorf("AverageBitrateBps mismatch: got %d, want %d", actualReport.AverageBitrateBps, expectedReport.AverageBitrateBps)
	}

	// Verify the filename ends with the expected path (since the full path may vary)
	expectedSuffix := filepath.Join("testdata", "video-mez-segment-1.mp4")
	if !strings.HasSuffix(actualReport.Filename, expectedSuffix) {
		t.Errorf("Filename doesn't end with expected suffix '%s': got %s", expectedSuffix, actualReport.Filename)
	}

	// Ensure no errors are present for this valid file
	if len(actualReport.DurationVariations) > 0 {
		t.Errorf("Expected no duration variations, but got %d", len(actualReport.DurationVariations))
	}

	if len(actualReport.SequenceNumberErrors) > 0 {
		t.Errorf("Expected no sequence number errors, but got %d", len(actualReport.SequenceNumberErrors))
	}

	if len(actualReport.TFDTTimeErrors) > 0 {
		t.Errorf("Expected no TFDT time errors, but got %d", len(actualReport.TFDTTimeErrors))
	}
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
	result, err := validate.ValidateMez(modifiedFile)
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
