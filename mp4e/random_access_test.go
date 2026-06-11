package mp4e

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

func TestEnsureRandomAccessFrameCodecAware(t *testing.T) {
	avcSample := mp4.FullSample{Data: sampleFromNALU([]byte{byte(avc.NALU_IDR), 0x00})}
	require.NoError(t, ensureRandomAccessFrame(avcSample, videoCodecAVC))

	hevcSample := mp4.FullSample{Data: sampleFromNALU([]byte{byte(hevc.NALU_IDR_N_LP) << 1, 0x01, 0x00})}
	require.NoError(t, ensureRandomAccessFrame(hevcSample, videoCodecHEVC))
	require.NoError(t, ensureRandomAccessFrame(hevcSample, videoCodecUnknown))
	err := ensureRandomAccessFrame(hevcSample, videoCodecAVC)
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "IDR random access slice NAL unit not found"))
}

func sampleFromNALU(nalu []byte) []byte {
	sample := make([]byte, 4+len(nalu))
	binary.BigEndian.PutUint32(sample[:4], uint32(len(nalu)))
	copy(sample[4:], nalu)
	return sample
}
