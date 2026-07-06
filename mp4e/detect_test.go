package mp4e

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsMP4Extension(t *testing.T) {
	for _, p := range []string{"a.mp4", "A.MP4", "clip.m4v", "song.m4a", "movie.mov", "/x/y/z.MoV"} {
		require.True(t, IsMP4Extension(p), p)
	}
	for _, p := range []string{"a.mxf", "a.wav", "a.mkv", "a.ts", "noext", "a.mp3"} {
		require.False(t, IsMP4Extension(p), p)
	}
}

func TestIsMP4FormatName(t *testing.T) {
	// FFmpeg reports the whole ISOBMFF family under one name.
	require.True(t, IsMP4FormatName("mov,mp4,m4a,3gp,3g2,mj2"))
	require.False(t, IsMP4FormatName("mxf"))
	require.False(t, IsMP4FormatName("wav"))
	require.False(t, IsMP4FormatName("matroska,webm"))
}

func TestHasFtypHeader(t *testing.T) {
	ftyp := []byte{0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	require.True(t, HasFtypHeader(bytes.NewReader(ftyp)))
	require.False(t, HasFtypHeader(bytes.NewReader([]byte("RIFF\x00\x00\x00\x00WAVE"))))
	require.False(t, HasFtypHeader(bytes.NewReader([]byte{0x00, 0x01}))) // too short
}

func TestHasISOBMFFHeader(t *testing.T) {
	box := func(typ string) []byte {
		b := []byte{0x00, 0x00, 0x00, 0x18, 0, 0, 0, 0, 'x', 'y'}
		copy(b[4:8], typ)
		return b
	}
	for _, typ := range []string{"ftyp", "styp", "moov", "moof", "mdat", "sidx", "free"} {
		require.True(t, HasISOBMFFHeader(bytes.NewReader(box(typ))), typ)
	}
	// non-ISOBMFF leaders
	require.False(t, HasISOBMFFHeader(bytes.NewReader([]byte("RIFF\x00\x00\x00\x00WAVE"))))
	require.False(t, HasISOBMFFHeader(bytes.NewReader([]byte{0x06, 0x0e, 0x2b, 0x34, 0x02, 0x05, 0x01, 0x01}))) // MXF
	require.False(t, HasISOBMFFHeader(bytes.NewReader([]byte{0x00, 0x01})))                                     // too short
}
