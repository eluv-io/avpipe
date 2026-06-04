package goavpipe

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe/avdesc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/jsonc"
)

func dolbyAtmosProbe(t *testing.T) *ProbeInfo {
	t.Helper()
	raw, err := os.ReadFile("../testdata/avprobe_dolby_atmos.jsonc")
	require.NoError(t, err)
	var p ProbeInfo
	require.NoError(t, json.Unmarshal(jsonc.ToJSON(raw), &p))
	//println(p.String())
	return &p
}

func TestStreamByCodecType_Audio(t *testing.T) {
	p := dolbyAtmosProbe(t)
	s := p.StreamByCodecType("audio")
	require.NotNil(t, s)
	assert.Equal(t, "eac3", s.CodecName)
	assert.True(t, s.DolbyAtmos)
	require.NotNil(t, s.MP4, "MP4 must be present for MP4 EAC-3 stream")
	require.NotNil(t, s.MP4.EC3, "MP4.EC3 must be present for Dolby Atmos stream")
	assert.True(t, s.MP4.EC3.JOC, "EC3.JOC must be true for Dolby Atmos")
	assert.Equal(t, uint16(0xF801), s.MP4.EC3.ChanMap)
	assert.Equal(t, 16, s.MP4.EC3.ComplexityIndex)
	assert.Equal(t, &avdesc.EC3Info{JOC: true, ChanMap: 0xF801, ComplexityIndex: 16}, s.MP4.EC3)
}

func TestStreamByCodecType_Video(t *testing.T) {
	p := dolbyAtmosProbe(t)
	s := p.StreamByCodecType("video")
	require.NotNil(t, s)
	assert.Equal(t, "h264", s.CodecName)
}

func TestStreamByCodecType_NotFound(t *testing.T) {
	p := dolbyAtmosProbe(t)
	assert.Nil(t, p.StreamByCodecType("subtitle"))
}

func TestStreamInfoAsArray_GapFilling(t *testing.T) {
	// Stream indices 0 and 2 present; index 1 is absent.
	input := []StreamInfo{
		{StreamIndex: 0, CodecType: "video"},
		{StreamIndex: 2, CodecType: "audio"},
	}
	a := StreamInfoAsArray(input)
	assert.Equal(t, 3, len(a))
	assert.Equal(t, 0, a[0].StreamIndex)
	assert.Equal(t, "video", a[0].CodecType)
	assert.Equal(t, 1, a[1].StreamIndex)
	assert.Equal(t, "unknown", a[1].CodecType) // gap filled with AVMEDIA_TYPE_UNKNOWN
	assert.Equal(t, 2, a[2].StreamIndex)
	assert.Equal(t, "audio", a[2].CodecType)
}

func TestStreamInfoAsArray_SingleStream(t *testing.T) {
	input := []StreamInfo{
		{StreamIndex: 0, CodecType: "video"},
	}
	a := StreamInfoAsArray(input)
	assert.Equal(t, 1, len(a))
	assert.Equal(t, "video", a[0].CodecType)
}

func TestStreamInfoAsArray_PreservesFields(t *testing.T) {
	// Verify that stream data is not lost during the conversion.
	input := []StreamInfo{
		{StreamIndex: 0, CodecType: "video", CodecName: "h264", Width: 1920, Height: 1080},
		{StreamIndex: 1, CodecType: "audio", CodecName: "aac", Channels: 2},
	}
	a := StreamInfoAsArray(input)
	assert.Equal(t, 2, len(a))
	assert.Equal(t, "h264", a[0].CodecName)
	assert.Equal(t, 1920, a[0].Width)
	assert.Equal(t, 1080, a[0].Height)
	assert.Equal(t, "aac", a[1].CodecName)
	assert.Equal(t, 2, a[1].Channels)
}
