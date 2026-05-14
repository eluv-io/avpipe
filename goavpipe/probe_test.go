package goavpipe

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
