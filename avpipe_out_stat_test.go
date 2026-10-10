package avpipe_test

import (
	"path"
	"slices"
	"sync"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outStatRecord is one call to OutputHandler.Stat.
type outStatRecord struct {
	ordinal        int // the ordinal the output was opened with
	srcStreamIndex int // the source stream index reported with the stat
	avType         goavpipe.AVType
	statType       goavpipe.AVStatType
}

// recordingOutputOpener wraps xc.FileOutputOpener and records every Stat call,
// so a test can assert what stream index avpipe reports with a stat, per output.
type recordingOutputOpener struct {
	inner *xc.FileOutputOpener

	m    sync.Mutex
	seen []outStatRecord
}

func (oo *recordingOutputOpener) Open(h, fd int64, ordinal, segIndex int,
	pts int64, outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	inner, err := oo.inner.Open(h, fd, ordinal, segIndex, pts, outType)
	if err != nil {
		return nil, err
	}
	return &recordingOutput{OutputHandler: inner, oo: oo, ordinal: ordinal}, nil
}

func (oo *recordingOutputOpener) records() []outStatRecord {
	oo.m.Lock()
	defer oo.m.Unlock()
	return slices.Clone(oo.seen)
}

type recordingOutput struct {
	goavpipe.OutputHandler
	oo      *recordingOutputOpener
	ordinal int
}

func (o *recordingOutput) Stat(srcStreamIndex int, avType goavpipe.AVType,
	statType goavpipe.AVStatType, statArgs interface{}) error {

	o.oo.m.Lock()
	o.oo.seen = append(o.oo.seen, outStatRecord{o.ordinal, srcStreamIndex, avType, statType})
	o.oo.m.Unlock()
	return o.OutputHandler.Stat(srcStreamIndex, avType, statType, statArgs)
}

// useIOHandler installs the global input and output openers for the rest of
// t, then restores the ones that were installed before.
func useIOHandler(t *testing.T, in goavpipe.InputOpener, out goavpipe.OutputOpener) {
	prevIn, prevOut := goavpipe.GetGlobalInputOpener(), goavpipe.GetGlobalOutputOpener()
	goavpipe.InitIOHandler(in, out)
	t.Cleanup(func() { goavpipe.InitIOHandler(prevIn, prevOut) })
}

// TestOutStatsReportSourceStreamIndex pins the contract documented on
// avpipe_stater_f: every output stat reports the source media stream index of
// the output it belongs to, not the output's ordinal.
//
// An audio output ("fsegment-audio<ordinal>-%05d.mp4") carries the decoder's
// audio_stream_index[ordinal], which is in source stream order whatever the
// order of audio_index. The check is per output, not per set of indices: a
// permutation of the right indices would pass a set comparison.
func TestOutStatsReportSourceStreamIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	tests := []struct {
		name   string
		url    string // default videoBigBuckBunny3AudioPath: video 0, audio 1-4
		xcType goavpipe.XcType
		// channelLayout is the encoder's channel layout, if not the source's.
		channelLayout string
		// filterDescriptor is the audio filter graph, for merge.
		filterDescriptor string
		audioIndex       []int32
		// sources[ordinal] is the source stream that audio output must report.
		sources []int
		bypass  bool
	}{
		{name: "ascending", audioIndex: []int32{1, 2, 3}, sources: []int{1, 2, 3}},
		{name: "unordered", audioIndex: []int32{3, 1, 2}, sources: []int{1, 2, 3}},
		// With no selection the decoder picks the first audio stream.
		{name: "no audio_index", audioIndex: nil, sources: []int{1}},
		// Bypass writes packets through do_bypass, not encode_frame.
		{name: "bypass", audioIndex: []int32{2}, sources: []int{2}, bypass: true},
		// One output mixed from audio 0 and 1 has no single source: -1.
		{name: "audio join", url: "./media/gabby_shading_2mono_1080p.mp4",
			xcType: goavpipe.XcAudioJoin, channelLayout: "stereo", audioIndex: []int32{0, 1},
			sources: []int{-1}},
		// Merge mixes audio 0 and 1 into one output, likewise with no single source.
		{name: "audio merge", url: "./media/gabby_shading_2mono_1080p.mp4",
			xcType: goavpipe.XcAudioMerge, channelLayout: "stereo", audioIndex: []int32{0, 1},
			filterDescriptor: "[0:0][0:1]amerge=inputs=2,pan=stereo|c0=c0|c1=c1[aout]",
			sources:          []int{-1}},
	}

	outStats := []goavpipe.AVStatType{
		goavpipe.AV_OUT_STAT_BYTES_WRITTEN,
		goavpipe.AV_OUT_STAT_FRAME_WRITTEN,
		goavpipe.AV_OUT_STAT_START_FILE,
		goavpipe.AV_OUT_STAT_END_FILE,
		goavpipe.AV_OUT_STAT_ENCODING_END_PTS,
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			url := tc.url
			if url == "" {
				url = videoBigBuckBunny3AudioPath
			}
			checkFileExists(t, url)
			xcType := tc.xcType
			if xcType == goavpipe.XcNone {
				xcType = goavpipe.XcAll
			}

			outputDir := path.Join(baseOutPath, fn(), tc.name)
			setupOutDir(t, outputDir)

			params := &goavpipe.XcParams{
				BypassTranscoding:   tc.bypass,
				Format:              "fmp4-segment",
				StartTimeTs:         0,
				DurationTs:          -1,
				StartSegmentStr:     "1",
				VideoSegDurationTs:  294912,
				AudioSegDurationTs:  1428480,
				Ecodec:              h264Codec,
				Ecodec2:             "aac",
				EncHeight:           720,
				EncWidth:            1280,
				XcType:              xcType,
				StreamId:            -1,
				SyncAudioToStreamId: -1,
				ForceKeyInt:         48,
				Url:                 url,
				AudioIndex:          tc.audioIndex,
				FilterDescriptor:    tc.filterDescriptor,
				DebugFrameLevel:     debugFrameLevel,
			}

			opener := &recordingOutputOpener{inner: &xc.FileOutputOpener{Dir: outputDir, Stats: &statsInfo}}
			useIOHandler(t, &xc.FileInputOpener{URL: url, Stats: &statsInfo}, opener)

			if tc.channelLayout != "" {
				params.ChannelLayout = avpipe.ChannelLayout(tc.channelLayout)
			}

			boilerXc(t, params)

			// reported[output][stat] is the set of stream indices reported.
			type outKey struct {
				avType  goavpipe.AVType
				ordinal int
			}
			reported := map[outKey]map[goavpipe.AVStatType]map[int]bool{}
			for _, r := range opener.records() {
				if r.avType != goavpipe.FMP4AudioSegment && r.avType != goavpipe.FMP4VideoSegment {
					continue
				}
				k := outKey{r.avType, r.ordinal}
				if r.avType == goavpipe.FMP4VideoSegment {
					k.ordinal = 0 // the video "ordinal" is a digit of the segment number
				}
				if reported[k] == nil {
					reported[k] = map[goavpipe.AVStatType]map[int]bool{}
				}
				if reported[k][r.statType] == nil {
					reported[k][r.statType] = map[int]bool{}
				}
				reported[k][r.statType][r.srcStreamIndex] = true
			}

			want := map[outKey]int{}
			if xcType&goavpipe.XcVideo != 0 {
				want[outKey{goavpipe.FMP4VideoSegment, 0}] = 0
			}
			for ordinal, src := range tc.sources {
				want[outKey{goavpipe.FMP4AudioSegment, ordinal}] = src
			}

			for k := range reported {
				_, ok := want[k]
				assert.True(t, ok, "unexpected output %s ordinal %d", k.avType.Name(), k.ordinal)
			}
			for k, src := range want {
				stats := reported[k]
				require.NotNil(t, stats, "no stats for output %s ordinal %d", k.avType.Name(), k.ordinal)
				for _, st := range outStats {
					require.NotEmpty(t, stats[st], "output %s ordinal %d: no %s",
						k.avType.Name(), k.ordinal, st.Name())
					assert.Equal(t, map[int]bool{src: true}, stats[st],
						"output %s ordinal %d: %s must report source stream %d",
						k.avType.Name(), k.ordinal, st.Name(), src)
				}
			}
		})
	}
}

// TestOutStatsCopyMpegtsReportNoSourceStreamIndex pins the copy_mpegts case of
// avpipe_stater_f: a copy_mpegts segment carries every stream, so its stats
// report -1, not an index derived from the output URL. The transcoded outputs
// beside it still report their source stream.
func TestOutStatsCopyMpegtsReportNoSourceStreamIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := "./media/bbb_sunflower_2160p_30fps_normal_2min.ts"
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	params := &goavpipe.XcParams{
		InputCfg: goavpipe.InputConfig{
			CopyMode:      goavpipe.CopyModeRemuxed,
			CopyPackaging: transport.RawTs,
		},
		Format:              "fmp4-segment",
		StartTimeTs:         0,
		DurationTs:          -1,
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec2:             "aac",
		EncHeight:           -1,
		EncWidth:            -1,
		XcType:              goavpipe.XcAudio,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Url:                 url,
		AudioIndex:          []int32{2},
		DebugFrameLevel:     debugFrameLevel,
	}

	opener := &recordingOutputOpener{inner: &xc.FileOutputOpener{Dir: outputDir, Stats: &statsInfo}}
	useIOHandler(t, &xc.FileInputOpener{URL: url, Stats: &statsInfo}, opener)

	boilerXc(t, params)

	// reported[avType][stat] is the set of stream indices reported.
	reported := map[goavpipe.AVType]map[goavpipe.AVStatType]map[int]bool{}
	for _, r := range opener.records() {
		if r.avType != goavpipe.MpegtsSegment && r.avType != goavpipe.FMP4AudioSegment {
			continue
		}
		if reported[r.avType] == nil {
			reported[r.avType] = map[goavpipe.AVStatType]map[int]bool{}
		}
		if reported[r.avType][r.statType] == nil {
			reported[r.avType][r.statType] = map[int]bool{}
		}
		reported[r.avType][r.statType][r.srcStreamIndex] = true
	}

	ts := reported[goavpipe.MpegtsSegment]
	require.NotNil(t, ts, "no stats for copy_mpegts segments")
	for _, st := range []goavpipe.AVStatType{goavpipe.AV_OUT_STAT_START_FILE, goavpipe.AV_OUT_STAT_END_FILE} {
		require.NotEmpty(t, ts[st], "copy_mpegts segments: no %s", st.Name())
	}
	for st, indices := range ts {
		assert.Equal(t, map[int]bool{-1: true}, indices,
			"copy_mpegts segments: %s must report -1", st.Name())
	}

	audio := reported[goavpipe.FMP4AudioSegment]
	require.NotNil(t, audio, "no stats for the audio output")
	for st, indices := range audio {
		assert.Equal(t, map[int]bool{2: true}, indices,
			"audio output: %s must report source stream 2", st.Name())
	}
}
