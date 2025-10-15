package avpipe

import (
	"testing"

	"github.com/fxamacker/cbor/v2"
)

func TestUnpackStreamProbesFromCBOR(t *testing.T) {
	// This is a simple test to verify the functionality works
	// You would replace this with actual CBOR data from your C function

	// Create test data that matches the expected structure
	testData := []StreamInfoCBOR{
		{
			StreamIndex: 0,
			StreamId:    1,
			CodecType:   1,  // video
			CodecID:     27, // H.264
			CodecName:   "h264",
			Width:       1920,
			Height:      1080,
			TimeBase: AVRational{
				Num: 1,
				Den: 25,
			},
			FrameRate: AVRational{
				Num: 25,
				Den: 1,
			},
		},
	}

	// Encode to CBOR
	cborData, err := cbor.Marshal(testData)
	if err != nil {
		t.Fatalf("Failed to marshal test data: %v", err)
	}

	// Now test unpacking
	streams, err := UnpackStreamProbesFromCBOR(cborData)
	if err != nil {
		t.Fatalf("Failed to unpack CBOR data: %v", err)
	}

	if len(streams) != 1 {
		t.Fatalf("Expected 1 stream, got %d", len(streams))
	}

	stream := streams[0]
	if stream.StreamIndex != 0 {
		t.Errorf("Expected stream index 0, got %d", stream.StreamIndex)
	}

	if stream.CodecType != "video" {
		t.Errorf("Expected codec type 'video', got '%s'", stream.CodecType)
	}

	if stream.Width != 1920 {
		t.Errorf("Expected width 1920, got %d", stream.Width)
	}

	if stream.Height != 1080 {
		t.Errorf("Expected height 1080, got %d", stream.Height)
	}

	t.Logf("Successfully unpacked stream: %+v", stream)
}
