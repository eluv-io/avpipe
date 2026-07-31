package mp4e

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/codec/ac4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ac4DumpFile points TestAC4Dump at an arbitrary AC-4 MP4 to inspect, e.g.:
//
//	go test ./mp4e/ -run TestAC4Dump -v \
//	    -ac4.file=/Users/peter/d/media/dolby/ac4-atmos/elv_ac4_atmos_bumblebee.mp4
var ac4DumpFile = flag.String("ac4.file", "",
	"path to an AC-4 MP4 to dump parsed info for (TestAC4Dump); empty skips the test")

// assetAC4 is a progressive (non-fragmented) 5.1 AC-4 audio-only file. Ground truth
// (verified out of band): 25 fps, 800 samples, an I-frame every 25 frames -> 32 total at
// samples 1,26,51,...,776, with a correct stss (container and bitstream agree).
const (
	assetAC4         = "../media/Audio_ID_6ch_128kbps_25fps_ac4.mp4" // AVC absent; AC4 5.1, progressive
	assetAC4Samples  = 800
	assetAC4IFrames  = 32
	assetAC4Interval = 25
)

// assetAC4Frag is a fragmented file whose AC-4 track (5.1.4) is sync-over-marked: the
// container flags every sample as a sync sample, though only every 25th frame is a true
// bitstream I-frame. This is unrelated content to assetAC4 — the matching I-frame counts
// are a coincidence of both being ~32 s of 25 fps AC-4, not a shared elementary stream.
const (
	assetAC4Frag        = "../media/Audio_ID_720p_50fps_h264_514ch_192kbps_ac4_fra.mp4" // AVC + AC4 5.1.4, fragmented
	assetAC4FragIFrames = 32
)

func TestAC4IFramesProgressive(t *testing.T) {
	f, err := os.ReadFile(assetAC4)
	require.NoError(t, err)

	// Unlimited: full set of true I-frames.
	tracks, err := AC4IFrames(bytes.NewReader(f), 0)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	tr := tracks[0]
	require.Len(t, tr.IFrames, assetAC4IFrames)
	for i, sync := range tr.IFrames {
		assert.Equal(t, i*assetAC4Interval+1, sync.SampleNumber, "I-frame %d sample number", i)
		assert.True(t, sync.IFrameGlobal)
		// Progressive file has a correct stss, so container and bitstream agree.
		assert.True(t, sync.ContainerSync, "I-frame %d container sync", i)
	}
	// A correctly-signaled file has zero mismatches in either direction.
	assert.Equal(t, 0, tr.ContainerSyncNotIFrame)
	assert.Equal(t, 0, tr.IFrameNotContainerSync)

	// Default limit stops early; counters are over the samples processed up to that point.
	tracks, err = AC4IFrames(bytes.NewReader(f), ac4.DefaultMaxIFrames)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	assert.Len(t, tracks[0].IFrames, ac4.DefaultMaxIFrames)
	// Stopped at the 20th I-frame (sample (20-1)*25+1), so fewer than all samples seen.
	assert.Equal(t, (ac4.DefaultMaxIFrames-1)*assetAC4Interval+1, tracks[0].SamplesProcessed)
}

// TestAC4IFramesFragmentedOvermarked proves the payoff: the container marks every sample
// as a sync sample, yet the bitstream has only a fraction that many true I-frames.
func TestAC4IFramesFragmentedOvermarked(t *testing.T) {
	f, err := os.ReadFile(assetAC4Frag)
	if err != nil {
		t.Skipf("fragmented asset not present: %v", err)
	}
	tracks, err := AC4IFrames(bytes.NewReader(f), 0)
	require.NoError(t, err)
	require.Len(t, tracks, 1, "one AC-4 track")
	tr := tracks[0]

	// Bitstream truth: far fewer I-frames than the container's all-sync signaling claims.
	require.Len(t, tr.IFrames, assetAC4FragIFrames)
	for _, sync := range tr.IFrames {
		assert.True(t, sync.IFrameGlobal)
		assert.True(t, sync.ContainerSync, "the container over-marks, so every sample reads sync")
	}
	// The container marks every sample sync, so every processed sample that is not a true
	// I-frame is over-marked, and none are under-marked. Derived from the results, not a
	// hard-coded count.
	assert.Equal(t, tr.SamplesProcessed-len(tr.IFrames), tr.ContainerSyncNotIFrame)
	assert.Equal(t, 0, tr.IFrameNotContainerSync)
}

// TestAC4IFramesESEquivalence builds an AC-4 elementary stream from the progressive
// file's samples (no external tooling) and asserts the ES scanner finds the same I-frame
// positions as the MP4 adapter — a true equivalence test on identical frame bytes.
func TestAC4IFramesESEquivalence(t *testing.T) {
	f, err := os.ReadFile(assetAC4)
	require.NoError(t, err)

	// MP4 adapter I-frame sample numbers -> expected 0-based ES indices.
	tracks, err := AC4IFrames(bytes.NewReader(f), 0)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	var wantIdx []int
	for _, s := range tracks[0].IFrames {
		wantIdx = append(wantIdx, s.SampleNumber-1)
	}

	// Wrap every AC-4 sample in a 0xAC40 sync frame to synthesize the ES.
	es := ac4ESFromProgressive(t, f)
	gotIdx, err := ac4.IFrameIndices(bytes.NewReader(es), 0)
	require.NoError(t, err)
	assert.Equal(t, wantIdx, gotIdx)
}

// TestAC4IFramesTruncatedCountsFrameErrors truncates the file mid-mdat (moov precedes
// mdat, so it still decodes) and verifies the scan runs to completion: samples past the
// cut are counted in FrameErrors and skipped, not fatal, and results are still returned.
func TestAC4IFramesTruncatedCountsFrameErrors(t *testing.T) {
	f, err := os.ReadFile(assetAC4)
	require.NoError(t, err)

	tracks, err := AC4IFrames(bytes.NewReader(f[:len(f)/2]), 0)
	require.NoError(t, err) // per-sample read failures are non-fatal
	require.Len(t, tracks, 1)
	tr := tracks[0]
	assert.Greater(t, tr.FrameErrors, 0, "reads past the truncation should be counted")
	assert.Less(t, tr.SamplesProcessed, assetAC4Samples)
	assert.Equal(t, assetAC4Samples, tr.SamplesProcessed+tr.FrameErrors,
		"every sample is either processed or counted as an error")
}

// TestAC4Dump prints the parsed AC-4 info for the file given via -ac4.file: the dac4
// codec/presentation info (from ExtractCodecInfo) and the per-track I-frame scan with its
// mismatch/error counters (from AC4IFrames). It is a diagnostic, not an assertion — it
// skips unless a file is provided and completes even on a partial scan.
func TestAC4Dump(t *testing.T) {
	if *ac4DumpFile == "" {
		t.Skip("set -ac4.file=<path> to dump parsed AC-4 info")
	}
	f, err := os.ReadFile(*ac4DumpFile)
	require.NoError(t, err)
	t.Logf("file: %s (%d bytes)", *ac4DumpFile, len(f))

	// dac4 codec / presentation info.
	infos, err := ExtractCodecInfo(bytes.NewReader(f))
	require.NoError(t, err)
	for _, ci := range infos {
		if ci.AC4 == nil {
			continue
		}
		t.Logf("codec string: %s", ci.AC4.MimeCodecString())
		b, err := json.MarshalIndent(ci.AC4, "", "  ")
		require.NoError(t, err)
		t.Logf("dac4 AC4Info:\n%s", b)
	}

	// Per-track I-frame scan (unlimited). Print results even if the scan hit a structural
	// error — AC4IFrames returns whatever it gathered.
	tracks, err := AC4IFrames(bytes.NewReader(f), 0)
	if err != nil {
		t.Logf("AC4IFrames error (partial results follow): %v", err)
	}
	for _, tr := range tracks {
		t.Logf("track %d: samplesProcessed=%d iframes=%d frameErrors=%d containerSyncNotIFrame=%d iframeNotContainerSync=%d",
			tr.TrackID, tr.SamplesProcessed, len(tr.IFrames), tr.FrameErrors,
			tr.ContainerSyncNotIFrame, tr.IFrameNotContainerSync)
		for _, s := range tr.IFrames {
			t.Logf("  I-frame sample=%d decodeTime=%d containerSync=%v",
				s.SampleNumber, s.DecodeTime, s.ContainerSync)
		}
	}
}

func ac4ESFromProgressive(t *testing.T, mp4Data []byte) []byte {
	t.Helper()
	file, err := mp4.DecodeFile(bytes.NewReader(mp4Data))
	require.NoError(t, err)
	var es bytes.Buffer
	for _, trak := range file.Moov.Traks {
		if !isAC4Trak(trak) {
			continue
		}
		stbl := trak.Mdia.Minf.Stbl
		n := stbl.Stsz.GetNrSamples()
		for nr := uint32(1); nr <= n; nr++ {
			ranges, err := trak.GetRangesForSampleInterval(nr, nr)
			require.NoError(t, err)
			raw := mp4Data[ranges[0].Offset : ranges[0].Offset+ranges[0].Size]
			es.Write([]byte{0xAC, 0x40, byte(len(raw) >> 8), byte(len(raw))})
			es.Write(raw)
		}
	}
	return es.Bytes()
}
