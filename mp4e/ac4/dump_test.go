// Package ac4_test holds the diagnostic dump test, which needs mp4e's
// ExtractCodecInfo alongside this package's I-frame scan. mp4e imports this
// package, so this must be an external test package to avoid an import cycle.
package ac4_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/avpipe/mp4e/ac4"
	"github.com/stretchr/testify/require"
)

// dumpFile points TestDump at an arbitrary AC-4 MP4 to inspect, e.g.:
//
//	go test ./mp4e/ac4/ -run TestDump -v \
//	    -ac4.file=$PWD/media/sample_ac4_atmos_10s.mp4
var dumpFile = flag.String("ac4.file", "",
	"path to an AC-4 MP4 to dump parsed info for (TestDump); empty skips the test")

// TestDump prints the parsed AC-4 info for the file given via -ac4.file: the dac4
// codec/presentation info (from mp4e.ExtractCodecInfo) and the per-track I-frame scan
// with its mismatch/error counters (from ac4.IFrames). It is a diagnostic, not an
// assertion — it skips unless a file is provided and completes even on a partial scan.
func TestDump(t *testing.T) {
	if *dumpFile == "" {
		t.Skip("set -ac4.file=<path> to dump parsed AC-4 info")
	}
	f, err := os.ReadFile(*dumpFile)
	require.NoError(t, err)
	t.Logf("file: %s (%d bytes)", *dumpFile, len(f))

	// dac4 codec / presentation info.
	infos, err := mp4e.ExtractCodecInfo(bytes.NewReader(f))
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
	// error — IFrames returns whatever it gathered.
	tracks, err := ac4.IFrames(bytes.NewReader(f), 0)
	if err != nil {
		t.Logf("IFrames error (partial results follow): %v", err)
	}
	for _, tr := range tracks {
		t.Logf("track %d: samplesProcessed=%d iframes=%d frameErrors=%d containerSyncNotIFrame=%d iframeNotContainerSync=%d",
			tr.TrackID, tr.SamplesProcessed, len(tr.IFrames), tr.FrameErrors,
			tr.ContainerSyncNotIFrame, tr.IFrameNotContainerSync)
		t.Logf("  edit: present=%v applied=%v unapplied=%q mediaTime=%d duration=%d",
			tr.Edit.Present, tr.Edit.Applied, tr.Edit.Unapplied, tr.Edit.MediaTime, tr.Edit.Duration)
		t.Logf("  samplesInEdit=%d samplesTrimmed=%d priming=%d basis=%q partialHeadTrim=%v partialTailTrim=%v",
			tr.SamplesInEdit, tr.SamplesTrimmed, tr.PrimingSamples, tr.PrimingBasis,
			tr.PartialHeadTrim, tr.PartialTailTrim)
		t.Logf("  presentedDuration=%d", tr.PresentedDuration)
		for _, s := range tr.IFrames {
			t.Logf("  I-frame sample=%d decodeTime=%d presentationTime=%d inEdit=%v containerSync=%v",
				s.SampleNumber, s.DecodeTime, s.PresentationTime, s.InEdit, s.ContainerSync)
		}
	}
}
