package xc_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/require"
)

const (
	dovi20BFramesCTSTestSource = "../media/sample_dv20_2560x1440_bframes.mp4"
	h265Codec                  = "libx265"
)

type abrBypassDashPTSResult struct {
	name         string
	url          string
	startPts     int64
	sourceFrames []xc.ABRFrameTiming
	outputFrames []xc.ABRFrameTiming
}

// TestMezBypassWithBframesAndCts tests mez bypass and ABR bypass using a DolbyVision MV-HEVC source with dense B-frame GOPs.
// The source is 24fps with IDR frames every 48 frames and each 30s part starts with an IDR frame that has non-zero CTS.
// This covers the dashenc.c fix to preserve source timing with B-frame reordering in DASH bypass output.
func TestMezBypassWithBframesAndCts(t *testing.T) {
	const (
		mezPartTimeScale     = int64(12288)
		mezPartDurationTs    = int64(30) * mezPartTimeScale
		mezFinalDurationTs   = int64(18) * mezPartTimeScale
		abrSegmentDurationTs = int64(2) * mezPartTimeScale
		mezPartFrameCount    = int32(30 * 24)
	)

	skipIfFileMissing(t, dovi20BFramesCTSTestSource)

	testDir := filepath.Join(testOutBase, "xc_test."+t.Name())
	mezDir := filepath.Join(testDir, "Mez")
	mezParts := runBFramesCTSMezBypass(t, dovi20BFramesCTSTestSource, mezDir, mezPartTimeScale)

	tests := []struct {
		name               string
		url                string
		startPts           int64
		durationTs         int64
		startSegmentStr    string
		startFragmentIndex int32
		expectedChunks     int
	}{
		{
			name:               "p1",
			url:                mezParts[0],
			startPts:           0,
			durationTs:         mezPartDurationTs,
			startSegmentStr:    "1",
			startFragmentIndex: 1,
			expectedChunks:     15,
		},
		{
			name:               "p2",
			url:                mezParts[1],
			startPts:           mezPartDurationTs,
			durationTs:         mezPartDurationTs,
			startSegmentStr:    "16",
			startFragmentIndex: 1 + mezPartFrameCount,
			expectedChunks:     15,
		},
		{
			name:               "p3",
			url:                mezParts[2],
			startPts:           2 * mezPartDurationTs,
			durationTs:         mezPartDurationTs,
			startSegmentStr:    "31",
			startFragmentIndex: 1 + 2*mezPartFrameCount,
			expectedChunks:     15,
		},
		{
			name:               "p4",
			url:                mezParts[3],
			startPts:           3 * mezPartDurationTs,
			durationTs:         mezFinalDurationTs,
			startSegmentStr:    "46",
			startFragmentIndex: 1 + 3*mezPartFrameCount,
			expectedChunks:     0,
		},
	}

	var previousResult abrBypassDashPTSResult
	var havePreviousResult bool
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			dashDir := filepath.Join(testDir, "DASH", tt.name)
			result := runABRBypassDashPTS(t, tt.url, dashDir, tt.startPts, tt.durationTs, tt.startSegmentStr, tt.startFragmentIndex, abrSegmentDurationTs, tt.expectedChunks)
			if havePreviousResult {
				err := xc.AssertABRBypassPartsContiguous(
					previousResult.name, previousResult.startPts, previousResult.sourceFrames, previousResult.outputFrames,
					result.name, result.startPts, result.sourceFrames, result.outputFrames)
				require.NoError(t, err)
			}
			previousResult = result
			havePreviousResult = true
		})
	}
}

func runBFramesCTSMezBypass(t *testing.T, source, mezDir string, mezPartTimeScale int64) []string {
	t.Helper()

	params := goavpipe.NewXcParams()
	params.Url = source
	params.BypassTranscoding = true
	params.Format = "fmp4-segment"
	params.DurationTs = -1
	params.StartPts = 0
	params.StartSegmentStr = "1"
	params.VideoBitrate = 30000000
	params.VideoSegDurationTs = -1
	params.SegDuration = "30.0000"
	params.ForceKeyInt = 48
	params.Ecodec = h265Codec
	params.Dcodec = "hevc"
	params.GPUIndex = -1
	params.EncHeight = 1440
	params.EncWidth = 2560
	params.XcType = goavpipe.XcVideo
	params.Seekable = true
	params.StreamId = -1
	params.SyncAudioToStreamId = -1
	params.VideoTimeBase = 24
	params.VideoLayout = int32(goavpipe.VideoLayoutMVHEVC)

	resetDir(t, mezDir)
	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: source},
		&xc.FileOutputOpener{Dir: mezDir},
	)
	err := avpipe.Xc(params)
	require.NoError(t, err, "mez Xc failed for %s", source)

	mezParts, err := filepath.Glob(filepath.Join(mezDir, "vsegment-*.mp4"))
	require.NoError(t, err)
	sort.Strings(mezParts)
	require.Len(t, mezParts, 4, "expected 30s, 30s, 30s and partial 18s mez video parts")

	expectedFirstCTS := int32(3 * (mezPartTimeScale / 24))
	for _, part := range mezParts {
		assertMezPartStartsWithIDRCTS(t, part, expectedFirstCTS)
	}
	return mezParts
}

func assertMezPartStartsWithIDRCTS(t *testing.T, part string, expectedFirstCTS int32) {
	t.Helper()

	frames, _, err := xc.SourceMP4FrameTimings(part)
	require.NoError(t, err)
	require.NotEmpty(t, frames, "generated mez part has no video frames: %s", part)

	first := frames[0]
	require.True(t, first.Sync, "%s first sample should be sync/IDR", part)
	require.Equal(t, "I", first.FrameType, "%s first sample frame type", part)
	require.Equal(t, uint64(0), first.DTS, "%s first sample DTS", part)
	require.Equal(t, int64(expectedFirstCTS), first.PTS, "%s first sample PTS", part)
	require.Equal(t, expectedFirstCTS, first.Composition, "%s first sample CTS/composition offset", part)
}

func runABRBypassDashPTS(
	t *testing.T,
	url string,
	dashDir string,
	startPts int64,
	durationTs int64,
	startSegmentStr string,
	startFragmentIndex int32,
	abrSegmentDurationTs int64,
	expectedChunks int,
) abrBypassDashPTSResult {
	t.Helper()

	skipIfFileMissing(t, url)

	params := goavpipe.NewXcParams()
	params.Url = url
	params.BypassTranscoding = true
	params.Format = "dash"
	params.DurationTs = durationTs
	params.StartPts = startPts
	params.StartSegmentStr = startSegmentStr
	params.VideoBitrate = 10400000
	params.VideoSegDurationTs = abrSegmentDurationTs
	params.StartFragmentIndex = startFragmentIndex
	params.ForceKeyInt = 48
	params.Ecodec = h265Codec
	params.Dcodec = ""
	params.GPUIndex = -1
	params.EncHeight = 1440
	params.EncWidth = 2560
	params.XcType = goavpipe.XcVideo
	params.Seekable = false
	params.StreamId = -1
	params.SyncAudioToStreamId = 0
	params.VideoLayout = int32(goavpipe.VideoLayoutMVHEVC)
	params.Profile = "main10"

	resetDir(t, dashDir)
	goavpipe.InitUrlIOHandler(url,
		&xc.FileInputOpener{URL: url},
		&xc.FileOutputOpener{Dir: dashDir})
	err := avpipe.Xc(params)
	require.NoError(t, err, "ABR Xc failed for %s", url)

	require.FileExists(t, filepath.Join(dashDir, "vinit-stream0.m4s"), "DASH init segment missing")
	chunks, err := filepath.Glob(filepath.Join(dashDir, "vchunk-stream0-*.m4s"))
	require.NoError(t, err)
	sort.Strings(chunks)
	if expectedChunks > 0 {
		require.Len(t, chunks, expectedChunks, "%s should produce %d DASH video chunks", url, expectedChunks)
	} else {
		require.NotEmpty(t, chunks, "%s should produce at least one DASH video chunk", url)
	}

	sourceFrames, sourceBFrameStats, err := xc.SourceMP4FrameTimings(url)
	require.NoError(t, err)
	output, err := xc.DashChunkFrameTimings(chunks)
	require.NoError(t, err)
	t.Logf("checked %d DASH chunks and %d video frames for frame-by-frame PTS continuity",
		output.ChunkCount, output.FrameCount)
	if len(output.ContinuityMismatches) > 0 {
		t.Errorf("DASH chunk DTS/PTS continuity mismatches: %d\n%s",
			len(output.ContinuityMismatches), strings.Join(output.ContinuityMismatches, "\n"))
	}
	require.NoError(t, xc.AssertDashMPDSegmentTimelineStart(dashDir, sourceFrames, startPts))
	fmt.Print(xc.SourceOutputPTSComparison(sourceFrames, output.Frames, sourceBFrameStats, startPts))
	require.NoError(t, xc.AssertSourceOutputFrameTimings(sourceFrames, output.Frames, startPts))

	return abrBypassDashPTSResult{
		name:         t.Name(),
		url:          url,
		startPts:     startPts,
		sourceFrames: sourceFrames,
		outputFrames: output.Frames,
	}
}

func resetDir(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.RemoveAll(dir))
	require.NoError(t, os.MkdirAll(dir, 0755))
}
