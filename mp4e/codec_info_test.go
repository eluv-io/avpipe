package mp4e

import (
	"bytes"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLevelName(t *testing.T) {
	// AVC: level_idc / 10
	assert.Equal(t, "3.1", LevelName("avc1", 31))
	assert.Equal(t, "4.0", LevelName("avc1", 40))
	assert.Equal(t, "4.0", LevelName("avc3", 40))
	// HEVC: level_idc / 30
	assert.Equal(t, "4.0", LevelName("hvc1", 120))
	assert.Equal(t, "4.1", LevelName("hev1", 123))
	// no level for AAC or EC-3
	assert.Equal(t, "", LevelName("mp4a", 2))
	assert.Equal(t, "", LevelName("ec-3", 0))
	// zero level_idc
	assert.Equal(t, "", LevelName("avc1", 0))
}

func TestProfileName(t *testing.T) {
	assert.Equal(t, "Baseline", ProfileName("avc1", 66))
	assert.Equal(t, "Main", ProfileName("avc1", 77))
	assert.Equal(t, "High", ProfileName("avc1", 100))
	assert.Equal(t, "High", ProfileName("avc3", 100))
	assert.Equal(t, "Main", ProfileName("hvc1", 1))
	assert.Equal(t, "Main 10", ProfileName("hvc1", 2))
	assert.Equal(t, "Main 10", ProfileName("hev1", 2))
	assert.Equal(t, "LC", ProfileName("mp4a", 2))
	assert.Equal(t, "", ProfileName("mp4a", 99))
	assert.Equal(t, "", ProfileName("ec-3", 0))
}

func TestExtractCodecInfo(t *testing.T) {
	avcInit, err := os.ReadFile("testdata/vinit-stream0.m4s")
	require.NoError(t, err)
	hevcInit, err := os.ReadFile("testdata/hevc-init.m4s")
	require.NoError(t, err)

	t.Run("AVC", func(t *testing.T) {
		infos, err := ExtractCodecInfo(bytes.NewReader(avcInit))
		require.NoError(t, err)
		require.Len(t, infos, 1)
		info := infos[0]
		require.Equal(t, "avc1", info.CodecID)
		require.NotEmpty(t, info.CodecParameter)
		require.True(t, len(info.CodecParameter) > len("avc1."),
			"codec descriptor must include profile/level: %s", info.CodecParameter)
		require.Equal(t, info.ProfileIDC, 100)
		require.Equal(t, info.LevelIDC, 31)
		//t.Logf("AVC codec: %s (profile=%d level=%d → %.1f)",
		//	info.CodecParameter, info.ProfileIDC, info.LevelIDC, float64(info.LevelIDC)/10)
	})

	t.Run("HEVC", func(t *testing.T) {
		infos, err := ExtractCodecInfo(bytes.NewReader(hevcInit))
		require.NoError(t, err)
		require.Len(t, infos, 1)
		info := infos[0]
		require.True(t, info.CodecID == "hvc1" || info.CodecID == "hev1",
			"expected hvc1 or hev1, got %s", info.CodecID)
		require.NotEmpty(t, info.CodecParameter)
		require.True(t, len(info.CodecParameter) > len("hvc1."),
			"codec descriptor must include profile/level: %s", info.CodecParameter)
		require.Equal(t, info.ProfileIDC, 2)
		require.Equal(t, info.LevelIDC, 120)
		//t.Logf("HEVC codec: %s (profile=%d level=%d → %.1f)",
		//	info.CodecParameter, info.ProfileIDC, info.LevelIDC, float64(info.LevelIDC)/30)
	})

	t.Run("MP4A", func(t *testing.T) {
		f, err := os.ReadFile("../media/Camindas.mp4")
		require.NoError(t, err)
		infos, err := ExtractCodecInfo(bytes.NewReader(f))
		require.NoError(t, err)
		require.Len(t, infos, 2)

		video := infos[0]
		assert.Equal(t, "avc1", video.CodecID)
		assert.Equal(t, "avc1.640028", video.CodecParameter)
		assert.Equal(t, 100, video.ProfileIDC)
		assert.Equal(t, 40, video.LevelIDC)

		audio := infos[1]
		assert.Equal(t, "mp4a", audio.CodecID)
		assert.Equal(t, "mp4a.40.2", audio.CodecParameter) // AAC-LC
		assert.Equal(t, 2, audio.ProfileIDC)                  // Audio Object Type 2 = AAC-LC
		assert.Equal(t, 2, audio.AudioChannels)
		assert.Nil(t, audio.EC3)
	})

	t.Run("Atmos", func(t *testing.T) {
		f, err := os.ReadFile("../media/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4")
		require.NoError(t, err)
		infos, err := ExtractCodecInfo(bytes.NewReader(f))
		require.NoError(t, err)
		require.Len(t, infos, 2)
		info := infos[0] // ec-3 is track 0
		assert.Equal(t, "ec-3", info.CodecID)
		assert.Equal(t, "ec-3", info.CodecParameter)
		assert.Equal(t, info.AudioChannels, 6)
		require.NotNil(t, info.EC3)
		assert.NotZero(t, info.EC3.ChanMap)
		assert.Equal(t, "L C R Ls Rs LFE", info.EC3.ChanMapString())
		assert.Equal(t, "F801", info.EC3.ChanMapHex())
		assert.True(t, info.EC3.JOC, "expected JOC flag for Dolby Atmos file")
		assert.Equal(t, info.EC3.ComplexityIndex, 16)
	})
}
