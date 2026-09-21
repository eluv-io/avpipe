package avpipe

import (
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func track(id int, mt avdesc.MediaType, tag string) *avdesc.MP4TrackInfo {
	return &avdesc.MP4TrackInfo{
		CodecInfo: avdesc.CodecInfo{CodecTagString: tag},
		TrackID:   id,
		MediaType: mt,
	}
}

// TestEnhanceStreamInfoSkippedTrack is the case positional matching gets wrong.
//
// mp4ff decodes a sample entry into a typed box only for codecs it knows, so a
// track it does not recognise is missing from the track list while FFmpeg still
// reports the stream. Here track 2 (in24 PCM, as in
// media/prores_bt709_bad_frame_color.mov) is absent, so walking both lists in
// step would hand the aac stream's info to the pcm stream and run off the end.
func TestEnhanceStreamInfoSkippedTrack(t *testing.T) {
	streams := []goavpipe.StreamInfo{
		{StreamIndex: 0, StreamId: 1, CodecType: "video", CodecName: "h264"},
		{StreamIndex: 1, StreamId: 2, CodecType: "audio", CodecName: "pcm_s24le"},
		{StreamIndex: 2, StreamId: 3, CodecType: "audio", CodecName: "aac"},
	}
	mov := &avdesc.MP4MovInfo{Tracks: []*avdesc.MP4TrackInfo{
		track(1, avdesc.MediaTypeVideo, "avc1"),
		// no track 2 - the box parser could not describe in24
		track(3, avdesc.MediaTypeAudio, "mp4a"),
	}}

	enhanceStreamInfo(streams, mov)

	require.NotNil(t, streams[0].MP4)
	assert.Equal(t, "avc1", streams[0].MP4.CodecTagString)

	// the undescribed stream keeps its FFmpeg-derived fields and gains nothing
	assert.Nil(t, streams[1].MP4, "pcm stream must not inherit another track's info")

	require.NotNil(t, streams[2].MP4)
	assert.Equal(t, "mp4a", streams[2].MP4.CodecTagString,
		"aac stream must get track 3, not be shifted by the missing track")
}

// TestEnhanceStreamInfoOutOfOrder pins that pairing does not depend on either
// list's order. A probe reporting streams in a different order from moov's
// traks is legal, and nothing guarantees the two agree.
func TestEnhanceStreamInfoOutOfOrder(t *testing.T) {
	streams := []goavpipe.StreamInfo{
		{StreamIndex: 0, StreamId: 3, CodecType: "audio"},
		{StreamIndex: 1, StreamId: 1, CodecType: "video"},
	}
	mov := &avdesc.MP4MovInfo{Tracks: []*avdesc.MP4TrackInfo{
		track(1, avdesc.MediaTypeVideo, "avc1"),
		track(3, avdesc.MediaTypeAudio, "ec-3"),
	}}

	enhanceStreamInfo(streams, mov)

	require.NotNil(t, streams[0].MP4)
	require.NotNil(t, streams[1].MP4)
	assert.Equal(t, "ec-3", streams[0].MP4.CodecTagString)
	assert.Equal(t, "avc1", streams[1].MP4.CodecTagString)
}

// TestEnhanceStreamInfoDataTrack covers a stream with no track of its own - a
// data or timecode track, which the box parser does not describe.
func TestEnhanceStreamInfoDataTrack(t *testing.T) {
	streams := []goavpipe.StreamInfo{
		{StreamIndex: 0, StreamId: 1, CodecType: "video"},
		{StreamIndex: 1, StreamId: 10, CodecType: "data"},
	}
	mov := &avdesc.MP4MovInfo{Tracks: []*avdesc.MP4TrackInfo{
		track(1, avdesc.MediaTypeVideo, "avc1"),
	}}

	enhanceStreamInfo(streams, mov)

	assert.NotNil(t, streams[0].MP4)
	assert.Nil(t, streams[1].MP4)
}

// TestEnhanceStreamInfoCodecTagWins pins the existing precedence: where the two
// sources disagree on the 4CC the box value is taken, because it is read from
// the sample entry rather than derived.
func TestEnhanceStreamInfoCodecTagWins(t *testing.T) {
	streams := []goavpipe.StreamInfo{
		{StreamIndex: 0, StreamId: 1, CodecType: "video", CodecTagString: "hev1"},
	}
	mov := &avdesc.MP4MovInfo{Tracks: []*avdesc.MP4TrackInfo{
		track(1, avdesc.MediaTypeVideo, "hvc1"),
	}}

	enhanceStreamInfo(streams, mov)
	assert.Equal(t, "hvc1", streams[0].CodecTagString)
}

func TestEnhanceStreamInfoNilMov(t *testing.T) {
	streams := []goavpipe.StreamInfo{{StreamIndex: 0, StreamId: 1, CodecType: "video"}}
	enhanceStreamInfo(streams, nil)
	assert.Nil(t, streams[0].MP4)
}
