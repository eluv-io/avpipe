package mp4e

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/goavpipe/avdesc"
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

func TestDOVICodecString(t *testing.T) {
	d := avdesc.DOVIInfo{Profile: 8, Level: 1, FourCC: "dvh1"}
	assert.Equal(t, "dvh1.08.01", d.CodecString())

	d2 := avdesc.DOVIInfo{Profile: 8, Level: 1, FourCC: "dvhe"}
	assert.Equal(t, "dvhe.08.01", d2.CodecString())

	d3 := avdesc.DOVIInfo{Profile: 5, Level: 13, FourCC: "dvh1"}
	assert.Equal(t, "dvh1.05.13", d3.CodecString())
}

func TestIsDOVIBoxType(t *testing.T) {
	assert.True(t, isDOVIBoxType("dvcC"))
	assert.True(t, isDOVIBoxType("dvvC"))
	assert.True(t, isDOVIBoxType("dvwC"))
	assert.False(t, isDOVIBoxType("hvcC"))
}

func TestParseDOVIBoxProfile20(t *testing.T) {
	// Profile 20 uses the dvwC box name in FFmpeg, but the payload layout is
	// the same Dolby Vision decoder configuration record as dvcC/dvvC.
	word := uint16(avdesc.DOVIProfileMVHEVC)<<9 | uint16(13)<<3 | 0b101
	payload := []byte{1, 0, byte(word >> 8), byte(word), 0x00}

	info, err := ParseDOVIBox(payload)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, 1, info.VersionMajor)
	assert.Equal(t, 0, info.VersionMinor)
	assert.Equal(t, avdesc.DOVIProfileMVHEVC, info.Profile)
	assert.Equal(t, 13, info.Level)
	assert.True(t, info.RPUPresent)
	assert.False(t, info.ELPresent)
	assert.True(t, info.BLPresent)
	assert.Equal(t, 0, info.BLSignalCompatibilityID)
}

func TestExtractCodecInfo_DOVI81(t *testing.T) {
	f, err := os.Open("testdata/dv81-hvc1-init.mp4")
	require.NoError(t, err)
	defer f.Close()

	infos, err := ExtractCodecInfo(f)
	require.NoError(t, err)
	require.Len(t, infos, 1)

	info := infos[0]
	assert.Equal(t, "hvc1", info.CodecTagString)
	assert.True(t, len(info.MimeCodecString) > len("hvc1."),
		"MimeCodecString must include profile/level: %s", info.MimeCodecString)
	assert.True(t, strings.HasPrefix(info.MimeCodecString, "hvc1."),
		"MimeCodecString must start with hvc1.: %s", info.MimeCodecString)

	require.NotNil(t, info.DOVI, "DOVI must be set for Dolby Vision file")
	assert.Equal(t, 8, info.DOVI.Profile, "expected DV profile 8")
	assert.Equal(t, 1, info.DOVI.Level, "expected DV level 1")
	assert.Equal(t, 1, info.DOVI.BLSignalCompatibilityID, "expected HDR10 compatibility (1)")
	assert.True(t, info.DOVI.RPUPresent, "expected RPU present flag")
	assert.False(t, info.DOVI.ELPresent, "expected EL not present (profile 8.1 is single-layer)")
	assert.True(t, info.DOVI.BLPresent, "expected BL present flag")
	assert.Equal(t, "dvh1", info.DOVI.FourCC, "hvc1 codec tag must map to dvh1 DV FourCC")
}

func TestExtractCodecInfo_HEVC_NoDoVI(t *testing.T) {
	// hevc-init.m4s is a non-DV HEVC fixture; DOVI must be nil
	hevcInit, err := os.ReadFile("testdata/hevc-init.m4s")
	require.NoError(t, err)

	infos, err := ExtractCodecInfo(bytes.NewReader(hevcInit))
	require.NoError(t, err)
	require.Len(t, infos, 1)

	info := infos[0]
	assert.True(t, info.CodecTagString == "hvc1" || info.CodecTagString == "hev1",
		"expected hvc1 or hev1, got %s", info.CodecTagString)
	assert.Nil(t, info.DOVI, "DOVI must be nil for non-DV HEVC file")
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
		require.Equal(t, "avc1", info.CodecTagString)
		require.NotEmpty(t, info.MimeCodecString)
		require.True(t, len(info.MimeCodecString) > len("avc1."),
			"codec descriptor must include profile/level: %s", info.MimeCodecString)
		require.Equal(t, 100, info.ProfileIDC)
		require.Equal(t, 31, info.Level)
		//t.Logf("AVC codec: %s (profile=%d level=%d → %.1f)",
		//	info.MimeCodecString, info.ProfileIDC, info.Level, float64(info.Level)/10)
	})

	t.Run("HEVC", func(t *testing.T) {
		infos, err := ExtractCodecInfo(bytes.NewReader(hevcInit))
		require.NoError(t, err)
		require.Len(t, infos, 1)
		info := infos[0]
		require.True(t, info.CodecTagString == "hvc1" || info.CodecTagString == "hev1",
			"expected hvc1 or hev1, got %s", info.CodecTagString)
		require.NotEmpty(t, info.MimeCodecString)
		require.True(t, len(info.MimeCodecString) > len("hvc1."),
			"codec descriptor must include profile/level: %s", info.MimeCodecString)
		require.Equal(t, 2, info.ProfileIDC)
		require.Equal(t, 120, info.Level)
		require.Equal(t, Mp4VideoLayoutMono, info.VideoLayout)
		require.Zero(t, info.EnhancementProfileIDC)
		//t.Logf("HEVC codec: %s (profile=%d level=%d → %.1f)",
		//	info.MimeCodecString, info.ProfileIDC, info.Level, float64(info.Level)/30)
	})

	t.Run("MP4A", func(t *testing.T) {
		f, err := os.ReadFile("../media/Camindas.mp4")
		require.NoError(t, err)
		infos, err := ExtractCodecInfo(bytes.NewReader(f))
		require.NoError(t, err)
		require.Len(t, infos, 2)

		video := infos[0]
		assert.Equal(t, "avc1", video.CodecTagString)
		assert.Equal(t, "avc1.640028", video.MimeCodecString)
		assert.Equal(t, 100, video.ProfileIDC)
		assert.Equal(t, 40, video.Level)

		audio := infos[1]
		assert.Equal(t, "mp4a", audio.CodecTagString)
		assert.Equal(t, "mp4a.40.2", audio.MimeCodecString) // AAC-LC
		assert.Equal(t, 2, audio.ProfileIDC)                // Audio Object Type 2 = AAC-LC
		assert.Equal(t, 2, audio.Channels)
		assert.Nil(t, audio.EC3)
	})

	t.Run("MV-HEVC SDR", func(t *testing.T) {
		f, err := os.ReadFile("../media/sample_mvhevc_4k.mp4")
		require.NoError(t, err)
		infos, err := ExtractCodecInfo(bytes.NewReader(f))
		require.NoError(t, err)
		require.Len(t, infos, 1)
		info := infos[0]
		assert.Equal(t, "hvc1", info.CodecTagString)
		assert.Equal(t, Mp4VideoLayoutMVHEVC, info.VideoLayout, "expected MV-HEVC layout")
		assert.Equal(t, 1, info.ProfileIDC)            // Main (base)
		assert.Equal(t, 6, info.EnhancementProfileIDC) // Multiview Main (enhancement)
		assert.Equal(t, 150, info.Level)
		// Comma-joined descriptor per RFC 6381 §3.4. Apple-style hvc1 prefix on
		// both halves; both halves include the trimmed-trailing-zero constraint
		// suffix produced by the standard HEVC codec-string algorithm.
		parts := strings.Split(info.MimeCodecString, ",")
		require.Len(t, parts, 2, "expected exactly two codec descriptors, got %q", info.MimeCodecString)
		assert.True(t, strings.HasPrefix(parts[0], "hvc1.1."),
			"base descriptor must be hvc1.1.* (Main), got %q", parts[0])
		assert.True(t, strings.HasPrefix(parts[1], "hvc1.6."),
			"enhancement descriptor must be hvc1.6.* (Multiview Main), got %q", parts[1])
		assert.Contains(t, parts[0], "L150")
		assert.Contains(t, parts[1], "L150")
		t.Logf("MV-HEVC SDR codec: %s", info.MimeCodecString)
	})

	t.Run("Atmos", func(t *testing.T) {
		f, err := os.ReadFile("../media/Audio_ID_720p_50fps_h264_6ch_640kbps_ddp_joc.mp4")
		require.NoError(t, err)
		infos, err := ExtractCodecInfo(bytes.NewReader(f))
		require.NoError(t, err)
		require.Len(t, infos, 2)
		info := infos[0] // ec-3 is track 0
		assert.Equal(t, "ec-3", info.CodecTagString)
		assert.Equal(t, "ec-3", info.MimeCodecString)
		assert.Equal(t, 6, info.Channels)
		require.NotNil(t, info.EC3)
		assert.NotZero(t, info.EC3.ChanMap)
		assert.Equal(t, "L C R Ls Rs LFE", info.EC3.ChanMapString())
		assert.Equal(t, "F801", info.EC3.ChanMapHex())
		assert.True(t, info.EC3.JOC, "expected JOC flag for Dolby Atmos file")
		assert.Equal(t, 16, info.EC3.ComplexityIndex)
	})
}
