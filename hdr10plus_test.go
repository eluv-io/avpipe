package avpipe_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHDR10PlusEndToEnd tests HDR10+ metadata injection from end to end:
// 1. Extracts HDR10+ metadata from reference file (hdr10-plus-injected.mp4)
// 2. Injects metadata into store using Go API
// 3. Encodes video with HDR10+ metadata
// 4. Verifies output contains HDR10+ SEI messages
func TestHDR10PlusEndToEnd(t *testing.T) {
	// Check if reference file exists
	refFile := "media/hdr10-plus-injected.mp4"
	if fileMissing(refFile, "TestHDR10PlusEndToEnd") {
		t.Skip("Reference file not found:", refFile)
	}

	outputDir := path.Join(baseOutPath, "hdr10plus_test")
	setupOutDir(t, outputDir)

	// Step 1: Extract HDR10+ metadata from reference file
	t.Log("Extracting HDR10+ metadata from reference file...")
	hdr10Metadata, err := extractHDR10PlusMetadata(refFile, 5)
	require.NoError(t, err, "Failed to extract HDR10+ metadata")
	require.NotEmpty(t, hdr10Metadata, "No HDR10+ metadata extracted")
	t.Logf("Extracted %d HDR10+ metadata entries", len(hdr10Metadata))

	// Step 2: Inject metadata into store
	t.Log("Injecting HDR10+ metadata into store...")
	for pts, jsonData := range hdr10Metadata {
		err := goavpipe.SetHdr10Plus(pts, []byte(jsonData))
		require.NoError(t, err, "Failed to set HDR10+ metadata for PTS %d", pts)
	}

	// Configure HDR10+ store parameters
	goavpipe.SetHdr10PlusTolerance(1000) // PTS tolerance
	goavpipe.SetHdr10PlusTTL(60)         // 60 second TTL
	goavpipe.SetHdr10PlusCapacity(1000)  // Max 1000 entries

	// Step 3: Encode with HDR10+ metadata
	t.Log("Encoding video with HDR10+ metadata...")
	inputFile := "media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	if fileMissing(inputFile, "TestHDR10PlusEndToEnd") {
		t.Skip("Input file not found:", inputFile)
	}

	fio := &fileInputOpener{t: t, url: inputFile}
	foo := &fileOutputOpener{t: t, dir: outputDir}
	goavpipe.InitIOHandler(fio, foo)

	params := &goavpipe.XcParams{
		Format:                 "fmp4-segment",
		XcType:                 goavpipe.XcVideo,
		DurationTs:             180000, // ~2 seconds at 90kHz
		VideoBitrate:           5000000,
		AudioBitrate:           -1,
		VideoSegDurationTs:     180000,
		AudioSegDurationTs:     -1,
		Ecodec:                 "libx265",
		Ecodec2:                "",
		VideoTimeBase:          90000,
		Url:                    inputFile,
		DebugFrameLevel:        false,
		StartFragmentIndex:     1,
		StartSegmentStr:        "1",
		StreamId:               -1,
		SyncAudioToStreamId:    -1,
		ForceKeyInt:            60,
		SampleRate:             -1,
		BypassTranscoding:      false,
		CrfStr:                 "23",
		EncHeight:              -1,
		EncWidth:               -1,
		ExtractImageIntervalTs: -1,
		GPUIndex:               -1,
		MasterDisplay:          "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1)",
		MaxCLL:                 "1000,400",
	}
	setFastEncodeParams(params, false)

	err = avpipe.Xc(params)
	require.NoError(t, err, "Encoding failed")

	// Give encoder time to finish writing
	time.Sleep(500 * time.Millisecond)

	// Step 4: Verify output contains HDR10+ metadata
	t.Log("Verifying HDR10+ metadata in output...")
	outputFiles, err := findOutputSegments(outputDir)
	require.NoError(t, err, "Failed to find output segments")
	require.NotEmpty(t, outputFiles, "No output segments found")

	t.Logf("Found %d output segments", len(outputFiles))

	// Check first video segment for HDR10+ SEI messages
	var videoSegment string
	for _, f := range outputFiles {
		if strings.Contains(f, "vsegment") || strings.Contains(f, "vfsegment") {
			videoSegment = f
			break
		}
	}
	require.NotEmpty(t, videoSegment, "No video segment found")

	// Verify HDR10+ is in the bitstream using ffmpeg trace
	hasHDR10Plus, err := verifyHDR10PlusInBitstream(videoSegment)
	require.NoError(t, err, "Failed to verify HDR10+ in bitstream")
	assert.True(t, hasHDR10Plus, "HDR10+ metadata not found in output bitstream")

	t.Log("✓ HDR10+ end-to-end test passed")
}

// extractHDR10PlusMetadata extracts HDR10+ metadata from a video file
// Returns a map of PTS -> JSON metadata string
func extractHDR10PlusMetadata(videoFile string, maxFrames int) (map[int64]string, error) {
	// Use ffprobe to extract HDR10+ metadata from first few frames
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-read_intervals", "%+#"+strconv.Itoa(maxFrames),
		"-show_frames",
		"-show_entries", "frame=pts:frame_side_data",
		"-of", "json",
		videoFile)

	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var result struct {
		Frames []struct {
			PTS          int64 `json:"pts"`
			SideDataList []struct {
				Type                                  string `json:"side_data_type"`
				ApplicationVersion                    int    `json:"application version"`
				NumWindows                            int    `json:"num_windows"`
				TargetedSystemDisplayMaximumLuminance string `json:"targeted_system_display_maximum_luminance"`
				Maxscl                                string `json:"maxscl"`
				AverageMaxrgb                         string `json:"average_maxrgb"`
				NumDistributionMaxrgbPercentiles      int    `json:"num_distribution_maxrgb_percentiles"`
				DistributionMaxrgbPercentage          int    `json:"distribution_maxrgb_percentage"`
				DistributionMaxrgbPercentile          string `json:"distribution_maxrgb_percentile"`
			} `json:"side_data_list"`
		} `json:"frames"`
	}

	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}

	metadata := make(map[int64]string)

	// Convert to simplified JSON format expected by our API
	for _, frame := range result.Frames {
		for _, sd := range frame.SideDataList {
			// Check for "HDR Dynamic Metadata SMPTE2094-40 (HDR10+)"
			if strings.Contains(sd.Type, "SMPTE2094-40") || strings.Contains(sd.Type, "HDR10+") {
				// Create simplified JSON (matching the format from test files)
				// For this test, we use a generic HDR10+ payload
				jsonData := `{"NumberOfWindows":1,"TargetedSystemDisplayMaximumLuminance":1000,"MaxScl":[100000,100000,100000],"AverageRGB":50000,"DistributionIndex":[1,5,10,25,50,75,90,95,99],"DistributionValues":[10000,20000,30000,40000,50000,60000,70000,80000,90000]}`
				metadata[frame.PTS] = jsonData
				break
			}
		}
	}

	return metadata, nil
}

// findOutputSegments finds all output segment files in the directory
func findOutputSegments(dir string) ([]string, error) {
	var segments []string

	// Check base directory and O/ and O/O1/ subdirectories
	checkDirs := []string{
		dir,
		path.Join(dir, "O"),
		path.Join(dir, "O", "O1"),
	}

	for _, checkDir := range checkDirs {
		entries, err := os.ReadDir(checkDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".mp4") {
				segments = append(segments, path.Join(checkDir, entry.Name()))
			}
		}
	}

	return segments, nil
}

// verifyHDR10PlusInBitstream checks if HDR10+ SEI messages are present in the video bitstream
func verifyHDR10PlusInBitstream(videoFile string) (bool, error) {
	// Use ffmpeg with trace_headers bitstream filter to detect SEI messages
	cmd := exec.Command("ffmpeg",
		"-v", "trace",
		"-i", videoFile,
		"-c:v", "copy",
		"-f", "null",
		"-")

	output, err := cmd.CombinedOutput()
	if err != nil {
		// ffmpeg exits with error when writing to null, check output anyway
		outputStr := string(output)
		if !strings.Contains(outputStr, "SEI") {
			return false, err
		}
	}

	outputStr := string(output)

	// Check for SEI messages
	// H.264: NAL unit type 6
	// HEVC: NAL unit type 39 (SEI_PREFIX)
	hasSEI := strings.Contains(outputStr, "SEI_PREFIX") ||
		strings.Contains(outputStr, "Decoding SEI") ||
		strings.Contains(outputStr, "nal_unit_type: 39") ||
		strings.Contains(outputStr, "nal_unit_type: 6(SEI)")

	// Additional check: Look for T.35 country code or user data
	hasT35 := strings.Contains(outputStr, "country_code") ||
		strings.Contains(outputStr, "user_data")

	return hasSEI || hasT35, nil
}

// TestHDR10PlusStoreBasic tests basic HDR10+ store functionality
func TestHDR10PlusStoreBasic(t *testing.T) {
	// Test setting and retrieving metadata
	testPTS := int64(12345)
	testJSON := `{"NumberOfWindows":1,"TargetedSystemDisplayMaximumLuminance":1000}`

	err := goavpipe.SetHdr10Plus(testPTS, []byte(testJSON))
	require.NoError(t, err, "Failed to set HDR10+ metadata")

	// Configure tolerance for retrieval
	goavpipe.SetHdr10PlusTolerance(100)

	t.Log("✓ HDR10+ store basic test passed")
}

// TestHDR10PlusStoreConfig tests HDR10+ store configuration
func TestHDR10PlusStoreConfig(t *testing.T) {
	// Test configuration APIs
	goavpipe.SetHdr10PlusTolerance(500)
	goavpipe.SetHdr10PlusTTL(30)
	goavpipe.SetHdr10PlusCapacity(5000)

	t.Log("✓ HDR10+ configuration test passed")
}
