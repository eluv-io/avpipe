package mp4e

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateHDRHEVCInit(t *testing.T) {
	f, err := os.Open("testdata/hevc-init.m4s")
	require.NoError(t, err)
	defer f.Close()

	mp4File, _, err := ValidateFmp4(f)
	require.NoError(t, err)

	hdr, err := ValidateHDR(mp4File)
	require.NoError(t, err)
	require.Len(t, hdr.Checks, 5)
	require.True(t, hdr.Checks[0].OK, hdr.Checks[0].Detail)
	require.False(t, hdr.Checks[1].OK, hdr.Checks[1].Detail)
	require.False(t, hdr.Checks[2].OK, hdr.Checks[2].Detail)
	require.False(t, hdr.Checks[3].OK, hdr.Checks[3].Detail)
	require.True(t, hdr.Checks[4].OK, hdr.Checks[4].Detail)
	require.NotNil(t, hdr.SEI.Mastering)
	require.NotNil(t, hdr.SEI.ContentLightLevel)

	info := hdr.FieldInfo()
	require.Equal(t, "hvc1", info.Codec)
	require.Equal(t, "Main10(2)", info.Profile)
	require.Equal(t, "10/10", info.BitDepth)
	require.Equal(t, "BT.2020(9)", info.ColorPrimaries)
	require.Equal(t, "PQ/ST2084(16)", info.TransferCharacteristics)
	require.Equal(t, "BT.2020 non-constant(9)", info.MatrixCoefficients)
	require.Equal(t, "limited", info.ColorRange)
	require.Equal(t, "G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(100000000,0)", info.MasteringDisplay)
	require.Equal(t, "0,0", info.MaxCLLFALL)
	require.Equal(t, "1023", info.MaxLuma)
	require.Equal(t, "0", info.MinLuma)
	require.True(t, strings.Contains(hdr.InfoString(), "info:\n"))
	require.True(t, strings.Contains(hdr.InfoString(), "  max_cll_fall: 0,0\n"))

	report := hdr.Report(true)
	require.NotNil(t, report.HDR.Track)
	require.Equal(t, "hvc1", report.HDR.Track.Codec)
	require.Len(t, report.HDR.Checks, 5)
	require.NotNil(t, report.Info)
	require.Equal(t, "0,0", report.Info.MaxCLLFALL)

	reportJSON, err := json.Marshal(report)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(reportJSON), `"hdr"`))
	require.True(t, strings.Contains(string(reportJSON), `"info"`))
	require.True(t, strings.Contains(string(reportJSON), `"max_cll_fall":"0,0"`))
}

func TestHDRFieldInfoUsesX265EncodingSettingsForLuma(t *testing.T) {
	settings := "x265 (build 199) options: master-display=G(8500,39850)B(6550,2300)R(35400,14600)WP(15635,16450)L(10000000,10) / cll=0,0 / min-luma=0 / max-luma=1023"
	minLuma, ok := x265EncodingSetting(settings, "min-luma")
	require.True(t, ok)
	require.Equal(t, "0", minLuma)
	maxLuma, ok := x265EncodingSetting(settings, "max-luma")
	require.True(t, ok)
	require.Equal(t, "1023", maxLuma)

	hdr := &HDRInfo{
		Mdcv: HDRMdcvInfo{
			Present:                      true,
			MaxDisplayMasteringLuminance: 10000000,
			MinDisplayMasteringLuminance: 10,
		},
		SEI: HDRSEIInfo{
			MinLuma: minLuma,
			MaxLuma: maxLuma,
		},
	}
	info := hdr.FieldInfo()
	require.Equal(t, "1023", info.MaxLuma)
	require.Equal(t, "0", info.MinLuma)
	require.Equal(t, "G(0,0)B(0,0)R(0,0)WP(0,0)L(10000000,10)", info.MasteringDisplay)
}
