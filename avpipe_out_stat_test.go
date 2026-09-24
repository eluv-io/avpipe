package avpipe_test

import (
	"path"
	"slices"
	"sync"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outStatRecord is one call to OutputHandler.Stat.
type outStatRecord struct {
	streamIndex int
	avType      goavpipe.AVType
	statType    goavpipe.AVStatType
}

// recordingOutputOpener wraps xc.FileOutputOpener and records every Stat call,
// so a test can assert what stream index avpipe reports with a stat.
type recordingOutputOpener struct {
	inner *xc.FileOutputOpener

	m    sync.Mutex
	seen []outStatRecord
}

func (oo *recordingOutputOpener) Open(h, fd int64, streamIndex, segIndex int,
	pts int64, outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	inner, err := oo.inner.Open(h, fd, streamIndex, segIndex, pts, outType)
	if err != nil {
		return nil, err
	}
	return &recordingOutput{OutputHandler: inner, oo: oo}, nil
}

func (oo *recordingOutputOpener) records() []outStatRecord {
	oo.m.Lock()
	defer oo.m.Unlock()
	return slices.Clone(oo.seen)
}

type recordingOutput struct {
	goavpipe.OutputHandler
	oo *recordingOutputOpener
}

func (o *recordingOutput) Stat(streamIndex int, avType goavpipe.AVType,
	statType goavpipe.AVStatType, statArgs interface{}) error {

	o.oo.m.Lock()
	o.oo.seen = append(o.oo.seen, outStatRecord{streamIndex, avType, statType})
	o.oo.m.Unlock()
	return o.OutputHandler.Stat(streamIndex, avType, statType, statArgs)
}

// TestOutStatBytesWrittenReportsSourceStreamIndex pins the contract documented on
// avpipe_stater_f: the stream_index carried by a stat is a *source* media stream
// index, not an output ordinal.
//
// AV_OUT_STAT_BYTES_WRITTEN used to break that. out_write_packet passed
// outctx->stream_index, which elv_io_open parses out of the segment filename
// ("fsegment-audio<i>-%05d.mp4"), so it was the audio output's ordinal.
// AV_OUT_STAT_FRAME_WRITTEN, reported from the encode loop, passed the source
// index all along. With audio_index = [1,2,3] the two disagreed for every audio
// output, and a consumer keying on the source index dropped the byte counts.
func TestOutStatBytesWrittenReportsSourceStreamIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow transcoding test in short mode")
	}
	url := videoBigBuckBunny3AudioPath
	checkFileExists(t, url)

	outputDir := path.Join(baseOutPath, fn())
	setupOutDir(t, outputDir)

	audioIndex := []int32{1, 2, 3}

	params := &goavpipe.XcParams{
		BypassTranscoding:   false,
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
		XcType:              goavpipe.XcAll,
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		ForceKeyInt:         48,
		Url:                 url,
		AudioIndex:          audioIndex,
		DebugFrameLevel:     debugFrameLevel,
	}

	opener := &recordingOutputOpener{inner: &xc.FileOutputOpener{Dir: outputDir, Stats: &statsInfo}}
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: url, Stats: &statsInfo}, opener)
	// Leave the shared handlers as the other tests expect to find them.
	defer goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: url, Stats: &statsInfo},
		&xc.FileOutputOpener{Dir: outputDir, Stats: &statsInfo})

	boilerXc(t, params)

	byStat := map[goavpipe.AVStatType]map[int]bool{}
	for _, r := range opener.records() {
		if r.avType != goavpipe.FMP4AudioSegment {
			continue
		}
		if byStat[r.statType] == nil {
			byStat[r.statType] = map[int]bool{}
		}
		byStat[r.statType][r.streamIndex] = true
	}

	bytesWritten := byStat[goavpipe.AV_OUT_STAT_BYTES_WRITTEN]
	require.NotEmpty(t, bytesWritten, "no audio AV_OUT_STAT_BYTES_WRITTEN reported")

	for idx := range bytesWritten {
		assert.True(t, slices.Contains(audioIndex, int32(idx)),
			"AV_OUT_STAT_BYTES_WRITTEN reported stream_index %d, which is not a "+
				"selected source stream (audio_index=%v) - it looks like an output ordinal",
			idx, audioIndex)
	}

	// The two output stats must agree, since both claim to carry a source index.
	if frameWritten := byStat[goavpipe.AV_OUT_STAT_FRAME_WRITTEN]; len(frameWritten) > 0 {
		assert.Equal(t, frameWritten, bytesWritten,
			"AV_OUT_STAT_FRAME_WRITTEN and AV_OUT_STAT_BYTES_WRITTEN disagree on the stream index")
	}
}
