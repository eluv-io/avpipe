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
// It also carries an edit list that trims nothing: one entry {media_time 0,
// segment_duration 1536000} whose end lands exactly on the last sample's end, so it
// exercises the tail boundary without excluding anything.
const (
	assetAC4         = "../media/Audio_ID_6ch_128kbps_25fps_ac4.mp4" // AVC absent; AC4 5.1, progressive
	assetAC4Samples  = 800
	assetAC4IFrames  = 32
	assetAC4Interval = 25
	assetAC4EditDur  = 1536000 // = 800 samples x 1920 ticks, mvhd and mdhd both 48000
)

// assetAC4Priming is the DEE-authored Atmos asset: 237 samples at 2048 ticks
// (frame_rate_index 13), an I-frame every 47 frames, and the only asset with a priming
// frame — elst {segment_duration 480000, media_time 2048} trims sample 1 at the head and
// sample 237 past the edit's end, leaving 235 presented frames = 5 x 47.
//
// Absolute path: not yet in gs://eluvio-test-assets, so tests using it skip when absent.
const (
	assetAC4Priming          = "/Users/peter/d/media/dolby/ac4-atmos/elv_ac4_atmos_bumblebee.mp4"
	assetAC4PrimingSamples   = 237
	assetAC4PrimingInEdit    = 235
	assetAC4PrimingFrameDur  = 2048
	assetAC4PrimingInterval  = 47
	assetAC4PrimingMediaTime = 2048
	assetAC4PrimingEditDur   = 480000
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

	// The edit list is bounded but trims nothing, so presentation equals decode time
	// throughout and no sample is excluded. The tail assertion is the load-bearing one: the
	// edit ends exactly where the last sample ends, so treating the end as inclusive would
	// wrongly trim sample 800.
	assert.True(t, tr.Edit.Present)
	assert.True(t, tr.Edit.Applied, "unapplied: %s", tr.Edit.Unapplied)
	assert.EqualValues(t, 0, tr.Edit.MediaTime)
	assert.EqualValues(t, assetAC4EditDur, tr.Edit.Duration)
	assert.Equal(t, assetAC4Samples, tr.SamplesInEdit)
	assert.Equal(t, 0, tr.SamplesTrimmed)
	assert.Equal(t, 0, tr.PrimingSamples)
	assert.Empty(t, tr.PrimingBasis, "an unprimed stream must not read as primed")
	assert.False(t, tr.PartialHeadTrim)
	for _, sync := range tr.IFrames {
		assert.True(t, sync.InEdit)
		assert.EqualValues(t, sync.DecodeTime, sync.PresentationTime,
			"media_time 0 leaves presentation == decode time")
	}

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

	// No edts at all: nothing to apply, so every sample is presented as stored. This also
	// covers the fragmented path's edit plumbing in its inactive state.
	assert.False(t, tr.Edit.Present)
	assert.False(t, tr.Edit.Applied)
	assert.Equal(t, tr.SamplesProcessed, tr.SamplesInEdit)
	assert.Equal(t, 0, tr.SamplesTrimmed)
	assert.Equal(t, 0, tr.PrimingSamples)
	assert.Empty(t, tr.PrimingBasis)
	for _, sync := range tr.IFrames {
		assert.True(t, sync.InEdit)
		assert.EqualValues(t, sync.DecodeTime, sync.PresentationTime)
	}
}

// TestAC4EditPrimingTrim is the edit-list payoff: on the one asset that has a priming
// frame, the scan reports presented audio rather than stored samples — the priming frame
// and the out-of-edit tail flagged as trimmed, presentation time rebased to the edit, and
// the priming count taken from the edit list rather than guessed.
//
// The presentation times are also the bridge to what avpipe emits. Applying the edit puts
// the presented I-frames at 0, 96256, 192512, ... which is exactly where AC4IFrames finds
// them (as decode times) in avpipe's edit-list-free bypass output. Asserting the times
// rather than two hard-coded sample-number lists keeps the two views tied together.
func TestAC4EditPrimingTrim(t *testing.T) {
	f, err := os.ReadFile(assetAC4Priming)
	if err != nil {
		t.Skipf("priming asset not present: %v", err)
	}
	tracks, err := AC4IFrames(bytes.NewReader(f), 0)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	tr := tracks[0]

	require.True(t, tr.Edit.Applied, "unapplied: %s", tr.Edit.Unapplied)
	assert.EqualValues(t, assetAC4PrimingMediaTime, tr.Edit.MediaTime)
	assert.EqualValues(t, assetAC4PrimingEditDur, tr.Edit.Duration)

	// One priming frame at the head and one sample past the edit's end at the tail.
	assert.Equal(t, assetAC4PrimingSamples, tr.SamplesProcessed)
	assert.Equal(t, assetAC4PrimingInEdit, tr.SamplesInEdit)
	assert.Equal(t, assetAC4PrimingSamples-assetAC4PrimingInEdit, tr.SamplesTrimmed)
	assert.Equal(t, 1, tr.PrimingSamples)
	assert.Equal(t, PrimingFromEdit, tr.PrimingBasis,
		"with an edit list present the count must be definitive, not inferred")
	assert.False(t, tr.PartialHeadTrim, "media_time 2048 is exactly one frame")

	// Sample 1 is the priming frame: an I-frame, but not presented. Its presentation time
	// is the negative pts the mov demuxer reports for the same sample (-2048), which is how
	// this reference implementation and avpipe's C path are checked against each other.
	require.NotEmpty(t, tr.IFrames)
	first := tr.IFrames[0]
	assert.Equal(t, 1, first.SampleNumber)
	assert.False(t, first.InEdit, "the priming frame is trimmed, not presented")
	assert.EqualValues(t, -assetAC4PrimingMediaTime, first.PresentationTime)

	// The presented I-frames sit at exact multiples of the I-frame interval from
	// presentation time 0 — the segment boundaries avpipe can cut on.
	var presented []AC4SampleSync
	for _, sync := range tr.IFrames {
		if sync.InEdit {
			presented = append(presented, sync)
		}
	}
	require.Equal(t, assetAC4PrimingInEdit/assetAC4PrimingInterval, len(presented))
	for i, sync := range presented {
		assert.EqualValues(t, i*assetAC4PrimingInterval*assetAC4PrimingFrameDur,
			sync.PresentationTime, "presented I-frame %d", i)
		// Same fact stated in sample numbers: renumbering the source's samples past the
		// priming frame gives the output's sample numbers.
		assert.Equal(t, i*assetAC4PrimingInterval+1, sync.SampleNumber-tr.PrimingSamples,
			"presented I-frame %d renumbered", i)
	}

	// The tail sample is an I-frame the edit excludes, and it is reported, not dropped.
	last := tr.IFrames[len(tr.IFrames)-1]
	assert.Equal(t, assetAC4PrimingSamples, last.SampleNumber)
	assert.False(t, last.InEdit, "the sample past the edit end is trimmed")
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
		t.Logf("  edit: present=%v applied=%v unapplied=%q mediaTime=%d duration=%d",
			tr.Edit.Present, tr.Edit.Applied, tr.Edit.Unapplied, tr.Edit.MediaTime, tr.Edit.Duration)
		t.Logf("  samplesInEdit=%d samplesTrimmed=%d priming=%d basis=%q partialHeadTrim=%v",
			tr.SamplesInEdit, tr.SamplesTrimmed, tr.PrimingSamples, tr.PrimingBasis,
			tr.PartialHeadTrim)
		for _, s := range tr.IFrames {
			t.Logf("  I-frame sample=%d decodeTime=%d presentationTime=%d inEdit=%v containerSync=%v",
				s.SampleNumber, s.DecodeTime, s.PresentationTime, s.InEdit, s.ContainerSync)
		}
	}
}

// TestAC4CadencePrimingOnAsset exercises the cadence heuristic on real bitstream data by
// neutralizing the priming asset's head trim: with media_time 0 the edit list no longer
// says anything about priming, but the priming frame is still there as sample 1. That is
// exactly the shape of an avpipe fmp4-segment mez, which writes no edts at all — the case
// the heuristic exists for, and one no checked-in asset provides.
//
// media_time is zeroed in place rather than by removing the edts, because this file has
// moov before mdat: dropping a box would shift mdat and invalidate every stco offset.
func TestAC4CadencePrimingOnAsset(t *testing.T) {
	f, err := os.ReadFile(assetAC4Priming)
	if err != nil {
		t.Skipf("priming asset not present: %v", err)
	}
	patched := ac4ZeroElstMediaTime(t, f)

	tracks, err := AC4IFrames(bytes.NewReader(patched), 0)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	tr := tracks[0]

	// The edit no longer trims at the head, so it cannot supply a priming count: sample 1
	// is presented, at presentation time 0. (The tail still trims — segment_duration is
	// untouched and now ends two samples early — which is irrelevant to priming.)
	require.True(t, tr.Edit.Applied, "unapplied: %s", tr.Edit.Unapplied)
	assert.EqualValues(t, 0, tr.Edit.MediaTime)
	require.NotEmpty(t, tr.IFrames)
	assert.Equal(t, 1, tr.IFrames[0].SampleNumber)
	assert.True(t, tr.IFrames[0].InEdit, "media_time 0 trims nothing at the head")
	assert.EqualValues(t, 0, tr.IFrames[0].PresentationTime)

	// The cadence still gives it away: I-frames at samples 1 and 2, then every 47.
	assert.Equal(t, 1, tr.PrimingSamples)
	assert.Equal(t, PrimingFromCadence, tr.PrimingBasis)
}

// ac4ZeroElstMediaTime returns a copy of an MP4 with the first elst entry's media_time set
// to 0, leaving every byte offset unchanged. Version 0 layout from the 'elst' type marker:
// +4 version/flags, +8 entry_count, +12 segment_duration, +16 media_time (all 32-bit).
func ac4ZeroElstMediaTime(t *testing.T, data []byte) []byte {
	t.Helper()
	i := bytes.Index(data, []byte("elst"))
	require.GreaterOrEqual(t, i, 0, "asset must have an elst to patch")
	require.Equal(t, byte(0), data[i+4], "only elst version 0 has 32-bit fields")

	out := append([]byte(nil), data...)
	copy(out[i+16:i+20], []byte{0, 0, 0, 0})
	return out
}

// ac4TrakWithEdit builds the minimum trak parseAC4Edit reads: a media timescale and an
// optional edit list. Nil entries means no edts at all.
func ac4TrakWithEdit(mediaTimescale uint32, entries []mp4.ElstEntry) *mp4.TrakBox {
	trak := &mp4.TrakBox{
		Mdia: &mp4.MdiaBox{Mdhd: &mp4.MdhdBox{Timescale: mediaTimescale}},
	}
	if entries != nil {
		trak.Edts = &mp4.EdtsBox{Elst: []*mp4.ElstBox{{Entries: entries}}}
	}
	return trak
}

func TestParseAC4Edit(t *testing.T) {
	simple := func(dur uint64, mediaTime int64) mp4.ElstEntry {
		return mp4.ElstEntry{SegmentDuration: dur, MediaTime: mediaTime, MediaRateInteger: 1}
	}
	for _, tc := range []struct {
		name           string
		movieTimescale uint32
		mediaTimescale uint32
		entries        []mp4.ElstEntry
		wantPresent    bool
		wantApplied    bool
		wantUnapplied  string
		wantMediaTime  int64
		wantDuration   uint64
		wantBounded    bool
		wantEditEnd    int64
	}{{
		name: "no edts", movieTimescale: 48000, mediaTimescale: 48000, entries: nil,
	}, {
		// An edts holding no elst entry carries no information; not worth reporting.
		name: "empty elst", movieTimescale: 48000, mediaTimescale: 48000,
		entries: []mp4.ElstEntry{},
	}, {
		// movenc writes this for every fragmented output. It must be a no-op: a
		// segment_duration of 0 that bounded the edit would trim the whole track away.
		name: "movenc identity edit", movieTimescale: 48000, mediaTimescale: 48000,
		entries:     []mp4.ElstEntry{simple(0, 0)},
		wantPresent: true, wantApplied: true, wantBounded: false,
	}, {
		name: "dee priming trim", movieTimescale: 48000, mediaTimescale: 48000,
		entries:     []mp4.ElstEntry{simple(480000, 2048)},
		wantPresent: true, wantApplied: true, wantMediaTime: 2048,
		wantDuration: 480000, wantBounded: true, wantEditEnd: 482048,
	}, {
		// The whole reason segment_duration is converted: mvhd 1000 against mdhd 48000
		// (the timescale pairing the _fra asset actually has) means 10 s of presentation
		// is 480000 media ticks, not 10000.
		name: "movie timescale differs from media", movieTimescale: 1000, mediaTimescale: 48000,
		entries:     []mp4.ElstEntry{simple(10000, 2048)},
		wantPresent: true, wantApplied: true, wantMediaTime: 2048,
		wantDuration: 480000, wantBounded: true, wantEditEnd: 482048,
	}, {
		// Rounds to nearest rather than truncating, so an inexact ratio cannot end the
		// edit early and trim a sample that should be presented. 8/3 -> 2.667 -> 3;
		// truncation would give 2, so this case distinguishes the two.
		name: "inexact timescale ratio rounds up", movieTimescale: 3, mediaTimescale: 1,
		entries:     []mp4.ElstEntry{simple(8, 0)},
		wantPresent: true, wantApplied: true, wantDuration: 3, wantBounded: true, wantEditEnd: 3,
	}, {
		name: "multiple entries not applied", movieTimescale: 48000, mediaTimescale: 48000,
		entries:     []mp4.ElstEntry{simple(1000, 0), simple(1000, 5000)},
		wantPresent: true, wantUnapplied: "multiple edit list entries",
	}, {
		name: "empty edit not applied", movieTimescale: 48000, mediaTimescale: 48000,
		entries:     []mp4.ElstEntry{simple(480000, -1)},
		wantPresent: true, wantUnapplied: "empty edit (media_time -1)",
	}, {
		name: "non-unit rate not applied", movieTimescale: 48000, mediaTimescale: 48000,
		entries:     []mp4.ElstEntry{{SegmentDuration: 480000, MediaRateInteger: 2}},
		wantPresent: true, wantUnapplied: "media rate is not 1.0",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			trak := ac4TrakWithEdit(tc.mediaTimescale, tc.entries)
			edit, window := parseAC4Edit(trak, tc.movieTimescale)

			assert.Equal(t, tc.wantPresent, edit.Present)
			assert.Equal(t, tc.wantApplied, edit.Applied)
			assert.Equal(t, tc.wantUnapplied, edit.Unapplied)
			assert.Equal(t, tc.wantMediaTime, edit.MediaTime)
			assert.Equal(t, tc.wantDuration, edit.Duration)

			// A present-but-unapplied edit must leave the window inactive, so samples read
			// as stored rather than being silently mapped through a misread edit.
			assert.Equal(t, tc.wantApplied, window.applied)
			assert.Equal(t, tc.wantBounded, window.bounded)
			if tc.wantBounded {
				assert.Equal(t, tc.wantEditEnd, window.editEnd)
			}
		})
	}
}

func TestAC4EditWindowClassify(t *testing.T) {
	// media_time 2048, presented duration 480000 -> edit end 482048: the DEE asset's edit.
	trimming := ac4EditWindow{applied: true, mediaTime: 2048, editEnd: 482048, bounded: true}
	unbounded := ac4EditWindow{applied: true, mediaTime: 2048}

	for _, tc := range []struct {
		name        string
		window      ac4EditWindow
		decodeTime  uint64
		dur         uint32
		wantPT      int64
		wantClass   ac4SampleClass
		wantPartial bool
	}{{
		name:   "no edit applied leaves decode time alone",
		window: ac4EditWindow{}, decodeTime: 4096, dur: 2048,
		wantPT: 4096, wantClass: ac4InEdit,
	}, {
		name:   "priming frame ends exactly at the edit start",
		window: trimming, decodeTime: 0, dur: 2048,
		wantPT: -2048, wantClass: ac4TrimmedHead,
	}, {
		name:   "first presented sample starts at the edit start",
		window: trimming, decodeTime: 2048, dur: 2048,
		wantPT: 0, wantClass: ac4InEdit,
	}, {
		// The last sample straddles the edit end: kept whole, since neither a container
		// nor this scan can express a fraction of a sample.
		name:   "sample straddling the edit end is kept",
		window: trimming, decodeTime: 481280, dur: 2048,
		wantPT: 479232, wantClass: ac4InEdit,
	}, {
		name:   "sample starting at the edit end is trimmed",
		window: trimming, decodeTime: 482048, dur: 2048,
		wantPT: 480000, wantClass: ac4TrimmedTail,
	}, {
		name:   "sample past the edit end is trimmed",
		window: trimming, decodeTime: 483328, dur: 2048,
		wantPT: 481280, wantClass: ac4TrimmedTail,
	}, {
		// Unbounded (segment_duration 0): the head still trims, the tail never does.
		name:   "unbounded edit trims the head",
		window: unbounded, decodeTime: 0, dur: 2048,
		wantPT: -2048, wantClass: ac4TrimmedHead,
	}, {
		name:   "unbounded edit never trims the tail",
		window: unbounded, decodeTime: 1 << 40, dur: 2048,
		wantPT: 1<<40 - 2048, wantClass: ac4InEdit,
	}, {
		// media_time inside a sample: mov keeps it (with SKIP_SAMPLES) rather than
		// dropping it, and so does this, flagging the sub-sample trim.
		name:   "sample straddling the edit start is kept and flagged",
		window: trimming, decodeTime: 1024, dur: 2048,
		wantPT: -1024, wantClass: ac4InEdit, wantPartial: true,
	}, {
		// A zero-duration sample at the edit start is presented, not head-trimmed: the
		// "ends at or before media_time" rule alone would exclude it.
		name:   "zero duration sample at the edit start is presented",
		window: trimming, decodeTime: 2048, dur: 0,
		wantPT: 0, wantClass: ac4InEdit,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.window.classify(tc.decodeTime, tc.dur)
			assert.Equal(t, tc.wantPT, got.presentationTime)
			assert.Equal(t, tc.wantClass, got.class)
			assert.Equal(t, tc.wantPartial, got.partialHead)
		})
	}
}

func TestAC4CadencePriming(t *testing.T) {
	iframesAt := func(nrs ...int) []AC4SampleSync {
		out := make([]AC4SampleSync, 0, len(nrs))
		for _, nr := range nrs {
			out = append(out, AC4SampleSync{SampleNumber: nr, IFrameGlobal: true})
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		iframes []AC4SampleSync
		want    bool
	}{{
		// The signature: a priming frame written as ordinary content, then the cadence
		// anchor one sample later, then the real interval. This is what an avpipe mez
		// looked like before the bypass drop, and the mez has no edts to say so.
		name: "priming signature", iframes: iframesAt(1, 2, 49, 96), want: true,
	}, {
		name: "clean stream at interval 47", iframes: iframesAt(1, 48, 95, 142), want: false,
	}, {
		// Every frame an I-frame: gaps of one throughout, which is not priming.
		name: "every frame an iframe", iframes: iframesAt(1, 2, 3, 4), want: false,
	}, {
		// Two I-frames one apart is not enough evidence on its own; a limit-truncated
		// scan must not fire the heuristic.
		name: "only two iframes", iframes: iframesAt(1, 2), want: false,
	}, {
		// A pair one apart later in the stream is not a priming frame.
		name: "pair not at the start", iframes: iframesAt(48, 49, 96, 143), want: false,
	}, {
		name: "no iframes", iframes: nil, want: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ac4CadencePriming(tc.iframes))
			// Whatever the cadence says, an edit list that already counted head-trimmed
			// samples wins and reports itself as the basis.
			track := AC4Track{IFrames: tc.iframes, PrimingSamples: 1}
			ac4FinalizePriming(&track)
			assert.Equal(t, PrimingFromEdit, track.PrimingBasis)
			assert.Equal(t, 1, track.PrimingSamples)

			track = AC4Track{IFrames: tc.iframes}
			ac4FinalizePriming(&track)
			if tc.want {
				assert.Equal(t, PrimingFromCadence, track.PrimingBasis)
				assert.Equal(t, 1, track.PrimingSamples)
			} else {
				assert.Empty(t, track.PrimingBasis)
				assert.Equal(t, 0, track.PrimingSamples)
			}
		})
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
