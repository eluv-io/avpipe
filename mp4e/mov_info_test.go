package mp4e

import (
	"bytes"
	"os"
	"path"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestExtractMovInfoLazy covers the layout half of a container description -
// the fields the sample entries and media headers carry beyond codec identity.
func TestExtractMovInfoLazy(t *testing.T) {
	t.Run("progressive file with ec-3 audio and avc video", func(t *testing.T) {
		b, err := os.ReadFile("../media/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4")
		require.NoError(t, err)

		mov, err := ExtractMovInfoLazy(bytes.NewReader(b))
		require.NoError(t, err)

		assert.Equal(t, "mp42", mov.MajorBrand)
		assert.Equal(t, []string{"isom", "mp42", "avc1"}, mov.CompatibleBrands)
		assert.Equal(t, 48000, mov.Timescale)
		assert.Equal(t, int64(1536000), mov.DurationTs)
		assert.False(t, mov.Fragmented)
		require.Len(t, mov.Tracks, 2)

		audio := mov.Tracks[0]
		assert.Equal(t, 1, audio.TrackID)
		assert.Equal(t, avdesc.MediaTypeAudio, audio.MediaType)
		assert.Equal(t, "ec-3", audio.CodecTagString)
		assert.Equal(t, 48000, audio.SampleRate)
		assert.Equal(t, 48000, audio.Timescale)
		// The source tags this track "eng", and it survives into mdhd. Note a
		// track avpipe *produced* would read "und": avpipe does not propagate
		// a source stream's language to its output.
		assert.Equal(t, "eng", audio.Language)

		// The caveat on MP4TrackInfo, demonstrated: the ec-3 sample entry
		// claims 2 channels and the dec3 box says 6, so a caller reading the
		// sample entry would under-report a 5.1 track as stereo.
		assert.Equal(t, 2, audio.ChannelCount)
		assert.Equal(t, 6, audio.Channels)
		assert.Equal(t, 6, audio.AudioChannels())
		require.NotNil(t, audio.EC3)
		assert.True(t, audio.EC3.JOC)

		video := mov.Tracks[1]
		assert.Equal(t, 2, video.TrackID)
		assert.Equal(t, avdesc.MediaTypeVideo, video.MediaType)
		assert.Equal(t, "avc1", video.CodecTagString)
		assert.Equal(t, 1280, video.Width)
		assert.Equal(t, 720, video.Height)
		// Per-track timescale, distinct from the movie timescale above
		assert.Equal(t, 50000, video.Timescale)
		assert.Zero(t, video.SampleRate)
		assert.Zero(t, video.ChannelCount)
	})

	t.Run("fragmented init segment", func(t *testing.T) {
		b, err := os.ReadFile("testdata/vinit-stream0.m4s")
		require.NoError(t, err)

		mov, err := ExtractMovInfoLazy(bytes.NewReader(b))
		require.NoError(t, err)

		// Fragmented and the brands are what let a caller confirm a buffer cut
		// at a box boundary is a usable init segment.
		assert.True(t, mov.Fragmented)
		assert.Equal(t, "iso5", mov.MajorBrand)
		assert.Equal(t, []string{"iso5", "iso6", "mp41"}, mov.CompatibleBrands)
		// The duration of a fragmented movie lives in its fragments, not mvhd
		assert.Zero(t, mov.DurationTs)

		require.Len(t, mov.Tracks, 1)
		video := mov.Tracks[0]
		assert.Equal(t, avdesc.MediaTypeVideo, video.MediaType)
		assert.Equal(t, 1280, video.Width)
		assert.Equal(t, 720, video.Height)
		assert.Equal(t, 30000, video.Timescale)
	})

	t.Run("not an MP4", func(t *testing.T) {
		_, err := ExtractMovInfoLazy(bytes.NewReader([]byte("not an mp4 at all, really")))
		require.Error(t, err)
	})
}

// TestExtractCodecInfoLazyProjectsMovInfo pins that the two extractors cannot
// disagree: ExtractCodecInfoLazy is the codec-identity projection of the same
// walk, not a second traversal that could drift from it.
func TestExtractCodecInfoLazyProjectsMovInfo(t *testing.T) {
	for _, path := range []string{
		"../media/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4",
		"testdata/vinit-stream0.m4s",
		"testdata/dv81-hvc1-init.mp4",
	} {
		t.Run(path, func(t *testing.T) {
			b, err := os.ReadFile(path)
			require.NoError(t, err)

			infos, err := ExtractCodecInfoLazy(bytes.NewReader(b))
			require.NoError(t, err)
			mov, err := ExtractMovInfoLazy(bytes.NewReader(b))
			require.NoError(t, err)

			require.Len(t, infos, len(mov.Tracks))
			for i, info := range infos {
				assert.Equal(t, mov.Tracks[i].CodecInfo, *info)
			}
		})
	}
}

// TestAudioChannels covers the resolution across codecs, because the two
// channel counts fail in opposite directions and a caller reading either field
// directly gets some of these wrong.
func TestAudioChannels(t *testing.T) {
	for _, tt := range []struct {
		path         string
		codec        string
		want         int
		channels     int // CodecInfo.Channels, from the codec config box
		channelCount int // the raw sample entry
	}{
		{
			// The parser decodes dec3, so CodecInfo.Channels is right and the
			// sample entry is a stub.
			path:  "../media/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4",
			codec: "ec-3", want: 6, channels: 6, channelCount: 2,
		},
		{
			// The parser does not decode dac4, so CodecInfo.Channels is empty
			// and the sample entry is the only source - the reverse of ec-3.
			path:  "../media/Audio_ID_6ch_128kbps_25fps_ac4.mp4",
			codec: "ac-4", want: 6, channels: 0, channelCount: 6,
		},
		{
			path:  "../media/Audio_ID_514ch_192kbps_25fps_ac4.mp4",
			codec: "ac-4", want: 10, channels: 0, channelCount: 10,
		},
		{
			path:  "../media/Audio_ID_2ch_64kbps_25fps_ac4.mp4",
			codec: "ac-4", want: 2, channels: 0, channelCount: 2,
		},
	} {
		t.Run(tt.codec+"_"+path.Base(tt.path), func(t *testing.T) {
			b, err := os.ReadFile(tt.path)
			require.NoError(t, err)

			mov, err := ExtractMovInfoLazy(bytes.NewReader(b))
			require.NoError(t, err)

			var audio *avdesc.MP4TrackInfo
			for _, tr := range mov.Tracks {
				if tr.MediaType == avdesc.MediaTypeAudio {
					audio = tr
					break
				}
			}
			require.NotNil(t, audio)
			assert.Equal(t, tt.codec, audio.CodecTagString)

			// pin both inputs, so a change to either parser shows up here
			assert.Equal(t, tt.channels, audio.Channels, "CodecInfo.Channels")
			assert.Equal(t, tt.channelCount, audio.ChannelCount, "sample entry")
			assert.Equal(t, tt.want, audio.AudioChannels(), "resolved")
		})
	}

	t.Run("video has no channels", func(t *testing.T) {
		b, err := os.ReadFile("../media/vsegment_head_24fps_ts12288.mp4")
		require.NoError(t, err)
		mov, err := ExtractMovInfoLazy(bytes.NewReader(b))
		require.NoError(t, err)
		require.NotEmpty(t, mov.Tracks)
		assert.Equal(t, avdesc.MediaTypeVideo, mov.Tracks[0].MediaType)
		assert.Zero(t, mov.Tracks[0].AudioChannels())
	})
}
