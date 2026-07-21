package mp4e

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Eyevinn/mp4ff/mp4"

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
	assert.True(t, IsDOVIBoxType("dvcC"))
	assert.True(t, IsDOVIBoxType("dvvC"))
	assert.True(t, IsDOVIBoxType("dvwC"))
	assert.False(t, IsDOVIBoxType("hvcC"))
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

// TestExtractCodecInfo_AC4 verifies AC-4 (dac4) parsing and the RFC 6381 codec
// string ac-4.<bv>.<pv>.<mdcompat> across the Dolby AC-4 Online Delivery Kit
// samples: stereo, 5.1, 5.1.4 (immersive), and immersive stereo (IMS, which has
// a leading presentation_version=2). Ground truth verified against Bento4's
// mp4dump and the raw dac4 bytes.
func TestExtractCodecInfo_AC4(t *testing.T) {
	cases := []struct {
		name         string
		file         string
		wantMime     string
		wantBV       int
		wantPV       int
		wantMDCompat int
		wantNPres    int
		wantChannels int // 0 = do not assert
		// Deep presentation-DSI fields. wantMask/wantChMode/wantTopPairs are
		// ground-truthed against Bento4 mp4dump (presentation_channel_mask_v1,
		// dsi_presentation_ch_mode, pres_top_channel_pairs). wantAjoc/wantDolbyAtmos
		// exercise the derived methods: Atmos = A-JOC objects (non-IMS) or
		// dolby_atmos_indicator (IMS), NOT the raw immersive_audio_indicator bit.
		wantMask       uint32
		wantChMode     int
		wantTopPairs   int
		wantIMS        bool
		wantImmersive  bool
		wantAjoc       bool
		wantDolbyAtmos bool
		wantFrameRate  string // FrameRate().RatString(), e.g. "25" or "375/16"
		wantLayout     string
	}{
		{"stereo", "../media/Audio_ID_2ch_64kbps_25fps_ac4.mp4", "ac-4.02.01.00", 2, 1, 0, 1, 2, 0x01, 1, 0, false, false, false, false, "25", "stereo"},
		{"5.1", "../media/Audio_ID_6ch_128kbps_25fps_ac4.mp4", "ac-4.02.01.01", 2, 1, 1, 1, 6, 0x47, 4, 0, false, false, false, false, "25", "5.1"},
		// 5.1.4 is a channel-based height bed: immersive (TopPairs>0) but NOT Atmos.
		// This row is the regression guard for the immersive_audio_indicator bug.
		// ch_mode 12 is the "7.1.4" container; back_channels_present=false + 2 top
		// pairs refine it to the actual 5.1.4 layout.
		{"5.1.4", "../media/Audio_ID_514ch_192kbps_25fps_ac4.mp4", "ac-4.02.01.02", 2, 1, 2, 1, 0, 0x77, 12, 2, false, true, false, false, "25", "5.1.4"},
		// IMS pair: same codec string, discriminated only by dolby_atmos_indicator.
		{"ims-atmos", "../media/Audio_ID_ims_112kbps_25fps_ac4.mp4", "ac-4.02.02.00", 2, 2, 0, 2, 2, 0x01, 1, 0, true, true, false, true, "25", "stereo"},
		{"ims-nonatmos", "../media/sample_ac4_ims_nonatmos.mp4", "ac-4.02.02.00", 2, 2, 0, 2, 2, 0x01, 1, 0, true, true, false, false, "25", "stereo"},
		// A-JOC object audio: genuine non-IMS Dolby Atmos, detected via b_ajoc.
		// (frame_rate_index 13 at a 48 kHz base = 48000/2048 = 375/16 = 23.4375 fps.)
		// Object-based → no fixed speaker layout.
		{"atmos", "../media/sample_ac4_atmos.mp4", "ac-4.02.01.03", 2, 1, 3, 1, 2, 0x800000, -1, 0, false, true, true, true, "375/16", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := os.ReadFile(tc.file)
			require.NoError(t, err)
			infos, err := ExtractCodecInfo(bytes.NewReader(f))
			require.NoError(t, err)
			require.Len(t, infos, 1)
			info := infos[0]
			assert.Equal(t, "ac-4", info.CodecTagString)
			assert.Equal(t, tc.wantMime, info.MimeCodecString)
			require.NotNil(t, info.AC4)
			assert.Equal(t, tc.wantMime, info.AC4.MimeCodecString())
			assert.Equal(t, tc.wantBV, info.AC4.BitstreamVersion)
			assert.Equal(t, tc.wantPV, info.AC4.PresentationVersion)
			assert.Equal(t, tc.wantMDCompat, info.AC4.MDCompat)
			assert.Equal(t, tc.wantNPres, info.AC4.NPresentations)
			assert.Equal(t, 48000, info.AC4.SampleRate())
			assert.Equal(t, tc.wantFrameRate, info.AC4.FrameRate().RatString())
			if tc.wantChannels != 0 {
				assert.Equal(t, tc.wantChannels, info.Channels)
			}
			assert.Equal(t, tc.wantMask, info.AC4.PresentationChannelMask, "presentation_channel_mask_v1")
			// ChMode is *int: negative wantChMode means "expect nil" (object /
			// not-channel-coded), otherwise expect a pointer to that value.
			if tc.wantChMode < 0 {
				assert.Nil(t, info.AC4.ChMode, "dsi_presentation_ch_mode (object → nil)")
			} else {
				require.NotNil(t, info.AC4.ChMode, "dsi_presentation_ch_mode")
				assert.Equal(t, tc.wantChMode, *info.AC4.ChMode, "dsi_presentation_ch_mode")
			}
			assert.Equal(t, tc.wantTopPairs, info.AC4.TopChannelPairs, "pres_top_channel_pairs")
			assert.Equal(t, tc.wantIMS, info.AC4.IsIMS(), "IsIMS")
			assert.Equal(t, tc.wantImmersive, info.AC4.IsImmersive(), "IsImmersive")
			assert.Equal(t, tc.wantAjoc, info.AC4.Ajoc(), "Ajoc")
			assert.Equal(t, tc.wantDolbyAtmos, info.AC4.IsDolbyAtmos(), "IsDolbyAtmos")
			assert.Equal(t, tc.wantLayout, info.AC4.ChannelLayout(), "ChannelLayout")
			require.NotEmpty(t, info.AC4.Presentations)

			// Raw-structure spot checks: the model must reflect substream groups,
			// not just the derived flags.
			switch tc.name {
			case "atmos":
				g := info.AC4.Presentations[0].SubstreamGroups
				require.NotEmpty(t, g, "A-JOC presentation must carry substream groups")
				assert.False(t, g[0].ChannelCoded, "A-JOC group is object-coded")
				require.NotEmpty(t, g[0].Substreams)
				assert.True(t, g[0].Substreams[0].Ajoc, "b_ajoc must be captured")
			case "5.1.4":
				g := info.AC4.Presentations[0].SubstreamGroups
				require.NotEmpty(t, g, "channel bed must carry substream groups")
				assert.True(t, g[0].ChannelCoded, "5.1.4 group is channel-coded")
				require.NotEmpty(t, g[0].Substreams)
				assert.NotZero(t, g[0].Substreams[0].ChannelGroups, "channel groups must be captured")
			}

			t.Logf("%s: mask=%#x layout=%q topPairs=%d ajoc=%v atmos=%v ims=%v immersive=%v",
				tc.name, info.AC4.PresentationChannelMask, info.AC4.ChannelLayout(), info.AC4.TopChannelPairs,
				info.AC4.Ajoc(), info.AC4.IsDolbyAtmos(), info.AC4.IsIMS(), info.AC4.IsImmersive())
		})
	}
}

// TestBuildAC4Info_UnsupportedVersions verifies the parser errors on layouts it
// does not support rather than silently mis-decoding.
func TestBuildAC4Info_UnsupportedVersions(t *testing.T) {
	// ac4_dsi_version 0 (legacy ac4_dsi, Part 1) — unsupported.
	_, err := buildAC4Info(&mp4.Dac4Box{AC4DSIVersion: 0})
	require.Error(t, err, "ac4_dsi_version 0 must error")

	// A v1 box with a presentation_version 0 (legacy v0 struct) — unsupported.
	_, err = buildAC4Info(&mp4.Dac4Box{
		AC4DSIVersion:  1,
		NPresentations: 1,
		Presentations:  []mp4.AC4Presentation{{PresentationVersion: 0, PresentationData: []byte{0x00}}},
	})
	require.Error(t, err, "presentation_version 0 must error")

	// A v1 box with an unknown presentation_version (>2) — unsupported.
	_, err = buildAC4Info(&mp4.Dac4Box{
		AC4DSIVersion:  1,
		NPresentations: 1,
		Presentations:  []mp4.AC4Presentation{{PresentationVersion: 3, PresentationData: []byte{0x00}}},
	})
	require.Error(t, err, "presentation_version 3 must error")
}

// TestBuildAC4Info_TruncatedPresentation verifies the !ok fallback: a supported
// version whose DSI payload is too short to fully decode must degrade gracefully —
// no panic, no error, codec-string fields (version, mdcompat) still set from the
// first byte, and ChMode left nil (unknown, not a misleading Mono).
func TestBuildAC4Info_TruncatedPresentation(t *testing.T) {
	// One byte 0x1f = config_v1 3 (top 5 bits), mdcompat 7 (low 3 bits). The parser
	// reads config+mdcompat (8 bits) then overruns on b_presentation_id → ok=false.
	box := &mp4.Dac4Box{
		AC4DSIVersion:  1,
		NPresentations: 1,
		Presentations:  []mp4.AC4Presentation{{PresentationVersion: 1, PresentationData: []byte{0x1f}}},
	}
	info, err := buildAC4Info(box)
	require.NoError(t, err, "supported version must not error on a short payload")
	require.Len(t, info.Presentations, 1)
	p := info.Presentations[0]
	assert.Equal(t, 1, p.Version)
	assert.Equal(t, 7, p.MDCompat, "mdcompat from the first DSI byte")
	assert.Nil(t, p.ChMode, "ChMode must be nil on an incomplete parse, not a false Mono")
	assert.Nil(t, info.ChMode, "top-level ChMode mirrors the (nil) default presentation")
}

// TestAC4SampleRate verifies the derived output sample rate against ETSI TS
// 103 190-1 Table E.5d: base from fs_index, multiplied by the default
// presentation's dsi_sf_multiplier at a 48 kHz base. The local assets are all
// 48 kHz / multiplier 0, so this synthetic test is the only coverage of the
// 96/192 kHz branches.
func TestAC4SampleRate(t *testing.T) {
	// withMultiplier builds an AC4Info whose default presentation's first
	// substream carries the given dsi_sf_multiplier.
	withMultiplier := func(fsIndex, sfMult int) avdesc.AC4Info {
		return avdesc.AC4Info{
			FSIndex: fsIndex,
			Presentations: []avdesc.AC4Presentation{{
				SubstreamGroups: []avdesc.AC4SubstreamGroup{{
					Substreams: []avdesc.AC4Substream{{SFMultiplier: sfMult}},
				}},
			}},
		}
	}
	// 44.1 kHz base: no multiplier regardless of the substream field.
	assert.Equal(t, 44100, withMultiplier(0, 0).SampleRate())
	assert.Equal(t, 44100, withMultiplier(0, 1).SampleRate())
	// 48 kHz base: dsi_sf_multiplier 0/1/2 → 48/96/192 kHz; 3 is reserved.
	assert.Equal(t, 48000, withMultiplier(1, 0).SampleRate())
	assert.Equal(t, 96000, withMultiplier(1, 1).SampleRate())
	assert.Equal(t, 192000, withMultiplier(1, 2).SampleRate())
	assert.Equal(t, 0, withMultiplier(1, 3).SampleRate(), "reserved multiplier → unknown")
	// No decoded substreams (e.g. truncated parse) → base rate, no multiplier.
	assert.Equal(t, 48000, avdesc.AC4Info{FSIndex: 1}.SampleRate())
}

// TestAC4FrameRate verifies the exact-rational frame rate against ETSI TS
// 103 190-1 Table 83: NTSC rates as 1000/1001 fractions (no float rounding), the
// base-dependent index 13, and nil for reserved indices.
func TestAC4FrameRate(t *testing.T) {
	rat := func(idx, fsIndex int) string {
		fr := avdesc.AC4Info{FrameRateIndex: idx, FSIndex: fsIndex}.FrameRate()
		if fr == nil {
			return "nil"
		}
		return fr.RatString()
	}
	assert.Equal(t, "24000/1001", rat(0, 1)) // 23.976 — exact, unrepresentable as float
	assert.Equal(t, "25", rat(2, 1))
	assert.Equal(t, "30000/1001", rat(3, 1)) // 29.97
	assert.Equal(t, "120000/1001", rat(11, 1))
	assert.Equal(t, "375/16", rat(13, 1))    // 48000/2048 at a 48 kHz base
	assert.Equal(t, "11025/512", rat(13, 0)) // 44100/2048 at a 44.1 kHz base
	assert.Equal(t, "nil", rat(14, 1))       // reserved
	assert.Equal(t, "nil", rat(15, 1))       // reserved
}

// TestExtractCodecInfoLazyBoundsMemoryOnGarbage verifies parsing of a bogus header
// advertising a multi-GB body
func TestExtractCodecInfoLazyBoundsMemoryOnGarbage(t *testing.T) {
	// size = 0x7fffffff (~2GB) followed by a garbage box type and a little data.
	data := []byte{0x7f, 0xff, 0xff, 0xff, 0x2d, 0x85, 0x00, 0x1e, 0xde, 0xad, 0xbe, 0xef}
	infos, err := ExtractCodecInfoLazy(bytes.NewReader(data))
	require.Error(t, err)
	require.Nil(t, infos)
}

// TestLimitedReadSeeker verifies probe MP4 extract read cap
func TestLimitedReadSeeker(t *testing.T) {
	data := make([]byte, 1000)
	lr := newLimitedReadSeeker(bytes.NewReader(data), 100)

	// Seeks are free and must not consume the read budget.
	pos, err := lr.Seek(500, 0) // io.SeekStart
	require.NoError(t, err)
	require.Equal(t, int64(500), pos)
	pos, err = lr.Seek(0, 0)
	require.NoError(t, err)
	require.Equal(t, int64(0), pos)

	// Reads are capped: io.ReadAll accumulates at most the limit, then errors.
	buf := make([]byte, 4096)
	got, err := lr.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 100, got) // truncated to the remaining budget
	_, err = lr.Read(buf)
	require.Error(t, err) // budget exhausted
	require.Contains(t, err.Error(), "read limit")
}

// TestExtractCodecInfoLazyRewindsOnNonMP4 verifies probe header sniff
func TestExtractCodecInfoLazyRewindsOnNonMP4(t *testing.T) {
	r := bytes.NewReader([]byte("RIFF\x24\x00\x00\x00WAVEfmt some more bytes here"))
	infos, err := ExtractCodecInfoLazy(r)
	require.Error(t, err)
	require.Nil(t, infos)
	pos, seekErr := r.Seek(0, 1) // io.SeekCurrent
	require.NoError(t, seekErr)
	require.Equal(t, int64(0), pos)
}
