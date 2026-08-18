package ac4

import (
	"bytes"
	"os"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/codec/ac4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assetAC4 is a progressive (non-fragmented) 5.1 AC-4 audio-only file. Ground truth
// (verified out of band): 25 fps, 800 samples, an I-frame every 25 frames -> 32 total at
// samples 1,26,51,...,776, with a correct stss (container and bitstream agree).
// It also carries an edit list that trims nothing: one entry {media_time 0,
// segment_duration 1536000} whose end lands exactly on the last sample's end, so it
// exercises the tail boundary without excluding anything.
const (
	assetAC4         = "../../media/Audio_ID_6ch_128kbps_25fps_ac4.mp4" // AVC absent; AC4 5.1, progressive
	assetAC4Samples  = 800
	assetAC4IFrames  = 32
	assetAC4Interval = 25
	assetAC4EditDur  = 1536000 // = 800 samples x 1920 ticks, mvhd and mdhd both 48000
)

// assetAC4Atmos10s is the longer DEE-authored Atmos asset: 237 samples at 2048 ticks
// (frame_rate_index 13), an I-frame every 47 frames — elst {segment_duration 480000,
// media_time 2048} trims sample 1 at the head and sample 237 past the edit's end, leaving
// 235 presented frames = 5 x 47. Of the 7 I-frames, 5 are presented and both trimmed
// samples happen to be I-frames, which is the coverage it adds over assetAC4Atmos: an
// I-frame wholly past the edit end, reported rather than dropped.
//
// Same content as assetAC4Atmos otherwise — both are DEE Atmos at frame_rate_index 13 with a
// priming frame and a segment_duration that is not a frame multiple. Length and I-frame
// cadence are what differ, so the two are worth keeping as separate rows.
const (
	assetAC4Atmos10s          = "../../media/sample_ac4_atmos_10s.mp4"
	assetAC4Atmos10sSamples   = 237
	assetAC4Atmos10sInEdit    = 235
	assetAC4Atmos10sIFrames   = 7
	assetAC4Atmos10sFrameDur  = 2048
	assetAC4Atmos10sInterval  = 47
	assetAC4Atmos10sMediaTime = 2048
	assetAC4Atmos10sEditDur   = 480000
)

// assetAC4Atmos is the in-repo DEE-authored Atmos asset: 49 samples at 2048 ticks
// (frame_rate_index 13), an I-frame every 23 frames, a priming frame at the head, and an
// elst whose segment_duration is NOT a multiple of the frame length —
// {media_time 2048, segment_duration 96000} with 2048-tick frames, so the edit ends 1792
// ticks into sample 48 rather than on a boundary.
//
// It is the short counterpart to assetAC4Atmos10s for the partial-tail case:
// PresentedDuration is 96000 while SamplesInEdit x 2048 is 96256.
const (
	assetAC4Atmos          = "../../media/sample_ac4_atmos.mp4"
	assetAC4AtmosSamples   = 49
	assetAC4AtmosInEdit    = 47
	assetAC4AtmosIFrames   = 4
	assetAC4AtmosFrameDur  = 2048
	assetAC4AtmosInterval  = 23
	assetAC4AtmosMediaTime = 2048
	assetAC4AtmosEditDur   = 96000
)

// assetAC4Frag is a fragmented file whose AC-4 track (5.1.4) is sync-over-marked: the
// container flags every sample as a sync sample, though only every 25th frame is a true
// bitstream I-frame. This is unrelated content to assetAC4 — the matching I-frame counts
// are a coincidence of both being ~32 s of 25 fps AC-4, not a shared elementary stream.
const (
	assetAC4Frag        = "../../media/Audio_ID_720p_50fps_h264_514ch_192kbps_ac4_fra.mp4" // AVC + AC4 5.1.4, fragmented
	assetAC4FragIFrames = 32
)

func TestIFramesProgressive(t *testing.T) {
	f, err := os.ReadFile(assetAC4)
	require.NoError(t, err)

	// Unlimited: full set of true I-frames.
	tracks, err := IFrames(bytes.NewReader(f), 0)
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
	tracks, err = IFrames(bytes.NewReader(f), ac4.DefaultMaxIFrames)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	assert.Len(t, tracks[0].IFrames, ac4.DefaultMaxIFrames)
	// Stopped at the 20th I-frame (sample (20-1)*25+1), so fewer than all samples seen.
	assert.Equal(t, (ac4.DefaultMaxIFrames-1)*assetAC4Interval+1, tracks[0].SamplesProcessed)
}

// TestIFramesFragmentedOvermarked proves the payoff: the container marks every sample
// as a sync sample, yet the bitstream has only a fraction that many true I-frames.
func TestIFramesFragmentedOvermarked(t *testing.T) {
	f, err := os.ReadFile(assetAC4Frag)
	if err != nil {
		t.Skipf("fragmented asset not present: %v", err)
	}
	tracks, err := IFrames(bytes.NewReader(f), 0)
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

// editAsset is one asset's ground truth for TestEditApplied. Both assets are DEE-authored
// Atmos at frame_rate_index 13 with a priming frame and an elst whose segment_duration is
// not a frame multiple; they differ in length and I-frame cadence.
type editAsset struct {
	name string
	path string

	samples   int    // SamplesProcessed
	inEdit    int    // SamplesInEdit
	iframes   int    // I-frames in the file, including those the edit trims
	presented int    // I-frames the edit presents
	frameDur  uint64 // sample duration, mdhd ticks
	interval  int    // frames between presented I-frames
	mediaTime int64  // elst media_time
	editDur   uint64 // elst segment_duration, mdhd ticks

	// tailIFrame reports that the sample past the edit's end is itself an I-frame, so the
	// scan must report it with InEdit false rather than drop it. Only the longer asset has
	// one, which is the coverage it adds over the in-repo file.
	tailIFrame bool
}

var editAssets = []editAsset{{
	name: "in-repo Atmos", path: assetAC4Atmos,
	samples: assetAC4AtmosSamples, inEdit: assetAC4AtmosInEdit,
	iframes: assetAC4AtmosIFrames, presented: assetAC4AtmosIFrames - 1,
	frameDur: assetAC4AtmosFrameDur, interval: assetAC4AtmosInterval,
	mediaTime: assetAC4AtmosMediaTime, editDur: assetAC4AtmosEditDur,
}, {
	name: "DEE Atmos 10s", path: assetAC4Atmos10s,
	samples: assetAC4Atmos10sSamples, inEdit: assetAC4Atmos10sInEdit,
	iframes: assetAC4Atmos10sIFrames, presented: assetAC4Atmos10sIFrames - 2,
	frameDur: assetAC4Atmos10sFrameDur, interval: assetAC4Atmos10sInterval,
	mediaTime: assetAC4Atmos10sMediaTime, editDur: assetAC4Atmos10sEditDur,
	tailIFrame: true,
}}

// TestEditApplied is the edit-list payoff: the scan reports presented audio rather than
// stored samples — the priming frame and any out-of-edit tail flagged as trimmed,
// presentation time rebased to the edit, and the priming count taken from the edit list
// rather than guessed.
//
// It also covers the partial tail. Neither asset's segment_duration is a multiple of the
// frame length, so the edit ends inside the last presented sample. That sample is kept whole
// and counted in SamplesInEdit — matching what both ISOBMFF and the mov demuxer do — so
// SamplesInEdit x frame duration overstates the presented span, and PresentedDuration is the
// number a caller must use.
//
// The presentation times are the bridge to what avpipe emits: they are exactly where IFrames
// finds the I-frames (as decode times) in avpipe's edit-list-free bypass output, which
// TestAudioAC4BypassStructure asserts from the other side.
func TestEditApplied(t *testing.T) {
	for _, a := range editAssets {
		t.Run(a.name, func(t *testing.T) {
			f, err := os.ReadFile(a.path)
			require.NoError(t, err)

			tracks, err := IFrames(bytes.NewReader(f), 0)
			require.NoError(t, err)
			require.Len(t, tracks, 1)
			tr := tracks[0]

			require.True(t, tr.Edit.Applied, "unapplied: %s", tr.Edit.Unapplied)
			assert.EqualValues(t, a.mediaTime, tr.Edit.MediaTime)
			assert.EqualValues(t, a.editDur, tr.Edit.Duration)

			// One priming frame at the head; whole samples past the edit end at the tail.
			assert.Equal(t, a.samples, tr.SamplesProcessed)
			assert.Equal(t, a.inEdit, tr.SamplesInEdit)
			assert.Equal(t, a.samples-a.inEdit, tr.SamplesTrimmed)
			assert.Equal(t, 1, tr.PrimingSamples)
			assert.Equal(t, PrimingFromEdit, tr.PrimingBasis,
				"with an edit list present the count must be definitive, not inferred")
			assert.False(t, tr.PartialHeadTrim, "media_time is exactly one frame")

			// The edit end lands inside the last presented sample, which is kept and counted
			// in SamplesInEdit — hence the two durations differ.
			assert.True(t, tr.PartialTailTrim, "segment_duration is not a frame multiple")
			assert.EqualValues(t, a.editDur, tr.PresentedDuration,
				"PresentedDuration must equal the edit's segment_duration")
			assert.NotEqual(t, uint64(a.inEdit)*a.frameDur, tr.PresentedDuration,
				"the whole-sample product overstates the presented span - that is why the field exists")

			// Sample 1 is the priming frame: an I-frame, but not presented. Its presentation
			// time is the negative pts the mov demuxer reports for the same sample, which is
			// how this reference implementation and avpipe's C path are checked against each
			// other.
			require.Len(t, tr.IFrames, a.iframes)
			first := tr.IFrames[0]
			assert.Equal(t, 1, first.SampleNumber)
			assert.False(t, first.InEdit, "the priming frame is trimmed, not presented")
			assert.EqualValues(t, -a.mediaTime, first.PresentationTime)

			// The presented I-frames sit at exact multiples of the I-frame interval from
			// presentation time 0 — the segment boundaries avpipe can cut on.
			var presented []SampleSync
			for _, sync := range tr.IFrames {
				if sync.InEdit {
					presented = append(presented, sync)
				}
			}
			require.Len(t, presented, a.presented)
			for i, sync := range presented {
				assert.EqualValues(t, uint64(i*a.interval)*a.frameDur, sync.PresentationTime,
					"presented I-frame %d", i)
				// Same fact in sample numbers: renumbering the source's samples past the
				// priming frame gives the output's sample numbers.
				assert.Equal(t, i*a.interval+1, sync.SampleNumber-tr.PrimingSamples,
					"presented I-frame %d renumbered", i)
			}

			// An I-frame wholly past the edit end is reported, not dropped.
			if a.tailIFrame {
				tail := tr.IFrames[len(tr.IFrames)-1]
				assert.Equal(t, a.samples, tail.SampleNumber)
				assert.False(t, tail.InEdit, "the sample past the edit end is trimmed")
			}
		})
	}
}

// TestIFramesESEquivalence builds an AC-4 elementary stream from the progressive
// file's samples (no external tooling) and asserts the ES scanner finds the same I-frame
// positions as the MP4 adapter — a true equivalence test on identical frame bytes.
func TestIFramesESEquivalence(t *testing.T) {
	f, err := os.ReadFile(assetAC4)
	require.NoError(t, err)

	// MP4 adapter I-frame sample numbers -> expected 0-based ES indices.
	tracks, err := IFrames(bytes.NewReader(f), 0)
	require.NoError(t, err)
	require.Len(t, tracks, 1)
	var wantIdx []int
	for _, s := range tracks[0].IFrames {
		wantIdx = append(wantIdx, s.SampleNumber-1)
	}

	// Wrap every AC-4 sample in a 0xAC40 sync frame to synthesize the ES.
	es := esFromProgressive(t, f)
	gotIdx, err := ac4.IFrameIndices(bytes.NewReader(es), 0)
	require.NoError(t, err)
	assert.Equal(t, wantIdx, gotIdx)
}

// TestIFramesTruncatedCountsFrameErrors truncates the file mid-mdat (moov precedes
// mdat, so it still decodes) and verifies the scan runs to completion: samples past the
// cut are counted in FrameErrors and skipped, not fatal, and results are still returned.
func TestIFramesTruncatedCountsFrameErrors(t *testing.T) {
	f, err := os.ReadFile(assetAC4)
	require.NoError(t, err)

	tracks, err := IFrames(bytes.NewReader(f[:len(f)/2]), 0)
	require.NoError(t, err) // per-sample read failures are non-fatal
	require.Len(t, tracks, 1)
	tr := tracks[0]
	assert.Greater(t, tr.FrameErrors, 0, "reads past the truncation should be counted")
	assert.Less(t, tr.SamplesProcessed, assetAC4Samples)
	assert.Equal(t, assetAC4Samples, tr.SamplesProcessed+tr.FrameErrors,
		"every sample is either processed or counted as an error")
}

// TestCadencePrimingOnAsset exercises the cadence heuristic on real bitstream data by
// neutralizing the priming asset's head trim: with media_time 0 the edit list no longer
// says anything about priming, but the priming frame is still there as sample 1. That is
// exactly the shape of an avpipe fmp4-segment mez, which writes no edts at all — the case
// the heuristic exists for, and one no checked-in asset provides.
//
// media_time is zeroed in place rather than by removing the edts, because this file has
// moov before mdat: dropping a box would shift mdat and invalidate every stco offset.
func TestCadencePrimingOnAsset(t *testing.T) {
	f, err := os.ReadFile(assetAC4Atmos10s)
	if err != nil {
		t.Skipf("priming asset not present: %v", err)
	}
	patched := zeroElstMediaTime(t, f)

	tracks, err := IFrames(bytes.NewReader(patched), 0)
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

// zeroElstMediaTime returns a copy of an MP4 with the first elst entry's media_time set
// to 0, leaving every byte offset unchanged. Version 0 layout from the 'elst' type marker:
// +4 version/flags, +8 entry_count, +12 segment_duration, +16 media_time (all 32-bit).
func zeroElstMediaTime(t *testing.T, data []byte) []byte {
	t.Helper()
	i := bytes.Index(data, []byte("elst"))
	require.GreaterOrEqual(t, i, 0, "asset must have an elst to patch")
	require.Equal(t, byte(0), data[i+4], "only elst version 0 has 32-bit fields")

	out := append([]byte(nil), data...)
	copy(out[i+16:i+20], []byte{0, 0, 0, 0})
	return out
}

// trakWithEdit builds the minimum trak parseEdit reads: a media timescale and an
// optional edit list. Nil entries means no edts at all.
func trakWithEdit(mediaTimescale uint32, entries []mp4.ElstEntry) *mp4.TrakBox {
	trak := &mp4.TrakBox{
		Mdia: &mp4.MdiaBox{Mdhd: &mp4.MdhdBox{Timescale: mediaTimescale}},
	}
	if entries != nil {
		trak.Edts = &mp4.EdtsBox{Elst: []*mp4.ElstBox{{Entries: entries}}}
	}
	return trak
}

func TestParseEdit(t *testing.T) {
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
			trak := trakWithEdit(tc.mediaTimescale, tc.entries)
			edit, window := parseEdit(trak, tc.movieTimescale)

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

func TestEditWindowClassify(t *testing.T) {
	// media_time 2048, presented duration 480000 -> edit end 482048: the DEE asset's edit.
	trimming := editWindow{applied: true, mediaTime: 2048, editEnd: 482048, bounded: true}
	unbounded := editWindow{applied: true, mediaTime: 2048}

	for _, tc := range []struct {
		name            string
		window          editWindow
		decodeTime      uint64
		dur             uint32
		wantPT          int64
		wantClass       sampleClass
		wantPartialHead bool
		wantPartialTail bool
		wantPresented   uint64
	}{{
		name:   "no edit applied leaves decode time alone",
		window: editWindow{}, decodeTime: 4096, dur: 2048,
		wantPT: 4096, wantClass: inEdit, wantPresented: 2048,
	}, {
		name:   "priming frame ends exactly at the edit start",
		window: trimming, decodeTime: 0, dur: 2048,
		wantPT: -2048, wantClass: trimmedHead,
	}, {
		name:   "first presented sample starts at the edit start",
		window: trimming, decodeTime: 2048, dur: 2048,
		wantPT: 0, wantClass: inEdit, wantPresented: 2048,
	}, {
		// The last sample straddles the edit end. It is kept whole because this scan
		// reports stored samples; the trim it carries shows up in presentedDur (768 of
		// its 2048 ticks) rather than by reclassifying or shortening the sample.
		name:   "sample straddling the edit end is kept whole but flagged partial-tail",
		window: trimming, decodeTime: 481280, dur: 2048,
		wantPT: 479232, wantClass: inEdit, wantPartialTail: true, wantPresented: 768,
	}, {
		name:   "sample starting at the edit end is trimmed",
		window: trimming, decodeTime: 482048, dur: 2048,
		wantPT: 480000, wantClass: trimmedTail,
	}, {
		name:   "sample past the edit end is trimmed",
		window: trimming, decodeTime: 483328, dur: 2048,
		wantPT: 481280, wantClass: trimmedTail,
	}, {
		// Unbounded (segment_duration 0): the head still trims, the tail never does.
		name:   "unbounded edit trims the head",
		window: unbounded, decodeTime: 0, dur: 2048,
		wantPT: -2048, wantClass: trimmedHead,
	}, {
		name:   "unbounded edit never trims the tail",
		window: unbounded, decodeTime: 1 << 40, dur: 2048,
		wantPT: 1<<40 - 2048, wantClass: inEdit, wantPresented: 2048,
	}, {
		// media_time inside a sample: mov keeps it (with SKIP_SAMPLES) rather than
		// dropping it, and so does this, flagging the sub-sample trim.
		name:   "sample straddling the edit start is kept and flagged",
		window: trimming, decodeTime: 1024, dur: 2048,
		wantPT: -1024, wantClass: inEdit, wantPartialHead: true, wantPresented: 1024,
	}, {
		// A zero-duration sample at the edit start is presented, not head-trimmed: the
		// "ends at or before media_time" rule alone would exclude it. It contributes
		// nothing to the presented span.
		name:   "zero duration sample at the edit start is presented",
		window: trimming, decodeTime: 2048, dur: 0,
		wantPT: 0, wantClass: inEdit,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.window.classify(tc.decodeTime, tc.dur)
			assert.Equal(t, tc.wantPT, got.presentationTime)
			assert.Equal(t, tc.wantClass, got.class)
			assert.Equal(t, tc.wantPartialHead, got.partialHead, "partialHead")
			assert.Equal(t, tc.wantPartialTail, got.partialTail, "partialTail")
			assert.Equal(t, tc.wantPresented, got.presentedDur, "presentedDur")
		})
	}
}

func TestCadencePriming(t *testing.T) {
	iframesAt := func(nrs ...int) []SampleSync {
		out := make([]SampleSync, 0, len(nrs))
		for _, nr := range nrs {
			out = append(out, SampleSync{SampleNumber: nr, IFrameGlobal: true})
		}
		return out
	}
	for _, tc := range []struct {
		name    string
		iframes []SampleSync
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
			assert.Equal(t, tc.want, cadencePriming(tc.iframes))
			// Whatever the cadence says, an edit list that already counted head-trimmed
			// samples wins and reports itself as the basis.
			track := Track{IFrames: tc.iframes, PrimingSamples: 1}
			finalizePriming(&track)
			assert.Equal(t, PrimingFromEdit, track.PrimingBasis)
			assert.Equal(t, 1, track.PrimingSamples)

			track = Track{IFrames: tc.iframes}
			finalizePriming(&track)
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

func esFromProgressive(t *testing.T, mp4Data []byte) []byte {
	t.Helper()
	file, err := mp4.DecodeFile(bytes.NewReader(mp4Data))
	require.NoError(t, err)
	var es bytes.Buffer
	for _, trak := range file.Moov.Traks {
		if !isTrak(trak) {
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
