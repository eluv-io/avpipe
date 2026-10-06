package avpipe_test

import (
	"fmt"
	"math"
	"path"
	"sync"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// avNoPTSValue is AV_NOPTS_VALUE (INT64_MIN) as the uint64 the stat carries.
const avNoPTSValue = uint64(math.MaxInt64) + 1

// endPTSKey identifies an fmp4 segment output: video, or an audio ordinal.
type endPTSKey struct {
	avType  goavpipe.AVType
	ordinal int
}

// endPTSOutputOpener wraps xc.FileOutputOpener and records the
// AV_OUT_STAT_ENCODING_END_PTS values of fmp4 segments, per output.
type endPTSOutputOpener struct {
	inner *xc.FileOutputOpener

	m      sync.Mutex
	endPTS map[endPTSKey][]uint64
}

func (oo *endPTSOutputOpener) Open(h, fd int64, streamIndex, segIndex int,
	pts int64, outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	inner, err := oo.inner.Open(h, fd, streamIndex, segIndex, pts, outType)
	if err != nil {
		return nil, err
	}
	return &endPTSOutput{OutputHandler: inner, oo: oo, ordinal: streamIndex}, nil
}

type endPTSOutput struct {
	goavpipe.OutputHandler
	oo      *endPTSOutputOpener
	ordinal int
}

func (o *endPTSOutput) Stat(srcStreamIndex int, avType goavpipe.AVType,
	statType goavpipe.AVStatType, statArgs interface{}) error {

	if statType == goavpipe.AV_OUT_STAT_ENCODING_END_PTS &&
		(avType == goavpipe.FMP4AudioSegment || avType == goavpipe.FMP4VideoSegment) {
		k := endPTSKey{avType, o.ordinal}
		if avType == goavpipe.FMP4VideoSegment {
			k.ordinal = 0 // the video "ordinal" is a digit of the segment number
		}
		o.oo.m.Lock()
		o.oo.endPTS[k] = append(o.oo.endPTS[k], *statArgs.(*uint64))
		o.oo.m.Unlock()
	}
	return o.OutputHandler.Stat(srcStreamIndex, avType, statType, statArgs)
}

// TestOutStatEncodingEndPTS checks that every fmp4 segment reports the last PTS
// written to its output: a valid PTS that increases from segment to segment, for
// each output when there are several.
func TestOutStatEncodingEndPTS(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := videoBigBuckBunny3AudioPath // video 0, audio 1-4
	checkFileExists(t, url)

	tests := []struct {
		name       string
		xcType     goavpipe.XcType
		audioIndex []int32
		bypass     bool
	}{
		{name: "transcode video", xcType: goavpipe.XcVideo},
		{name: "transcode audio 1", xcType: goavpipe.XcAudio, audioIndex: []int32{1}},
		{name: "transcode audio 1 and 2", xcType: goavpipe.XcAudio, audioIndex: []int32{1, 2}},
		{name: "bypass video and audio 1", xcType: goavpipe.XcAll, audioIndex: []int32{1}, bypass: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
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
				ForceKeyInt:         48,
				XcType:              tc.xcType,
				StreamId:            -1,
				SyncAudioToStreamId: -1,
				Url:                 url,
				AudioIndex:          tc.audioIndex,
				DebugFrameLevel:     debugFrameLevel,
			}

			opener := &endPTSOutputOpener{
				inner:  &xc.FileOutputOpener{Dir: outputDir, Stats: &statsInfo},
				endPTS: map[endPTSKey][]uint64{},
			}
			goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url, Stats: &statsInfo}, opener)
			// Leave the shared handlers as the other tests expect to find them.
			defer goavpipe.InitIOHandler(
				&xc.FileInputOpener{URL: url, Stats: &statsInfo},
				&xc.FileOutputOpener{Dir: outputDir, Stats: &statsInfo})

			boilerXc(t, params)

			var want []endPTSKey
			if tc.xcType&goavpipe.XcVideo != 0 {
				want = append(want, endPTSKey{goavpipe.FMP4VideoSegment, 0})
			}
			for ordinal := range tc.audioIndex {
				want = append(want, endPTSKey{goavpipe.FMP4AudioSegment, ordinal})
			}
			require.Len(t, opener.endPTS, len(want), "end-PTS reported for %d outputs", len(opener.endPTS))

			for _, k := range want {
				endPTS := opener.endPTS[k]
				out := fmt.Sprintf("output %s ordinal %d", k.avType.Name(), k.ordinal)
				require.NotEmpty(t, endPTS, "%s: no AV_OUT_STAT_ENCODING_END_PTS", out)
				t.Logf("%s: %d segments, end PTS %v", out, len(endPTS), endPTS)
				for seg, pts := range endPTS {
					assert.NotEqual(t, avNoPTSValue, pts, "%s segment %d: end PTS is AV_NOPTS_VALUE", out, seg)
					if seg > 0 {
						assert.Greater(t, pts, endPTS[seg-1], "%s segment %d: end PTS does not advance", out, seg)
					}
				}
			}
		})
	}
}
