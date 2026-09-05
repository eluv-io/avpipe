package avpipe

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/require"
)

func TestVerticalDataReaderReadsLittleEndianRecordsAcrossShortReads(t *testing.T) {
	r := &trackingReadCloser{Reader: iotest.OneByteReader(bytes.NewReader([]byte{
		0x78, 0x56, 0x34, 0x12,
		0xef, 0xcd, 0xab, 0x90,
	}))}
	h := newVerticalDataReaderHandle(r)
	t.Cleanup(func() { require.NoError(t, h.release()) })

	value, ok, err := h.readValue()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint32(0x12345678), value)

	value, ok, err = h.readValue()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, uint32(0x90abcdef), value)

	_, ok, err = h.readValue()
	require.NoError(t, err)
	require.False(t, ok)
}

func TestVerticalDataReaderRejectsPartialRecord(t *testing.T) {
	h := newVerticalDataReaderHandle(&trackingReadCloser{Reader: bytes.NewReader([]byte{1, 2, 3})})
	t.Cleanup(func() { require.NoError(t, h.release()) })

	_, ok, err := h.readValue()
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.False(t, ok)
}

func TestVerticalDataReaderReleaseClosesOnce(t *testing.T) {
	r := &trackingReadCloser{Reader: bytes.NewReader(nil)}
	h := newVerticalDataReaderHandle(r)

	require.NoError(t, h.close())
	require.NoError(t, h.release())
	require.NoError(t, h.release())
	require.Equal(t, 1, r.closeCount)
}

func TestGetCParamsRejectsAmbiguousVerticalDataSource(t *testing.T) {
	r := &trackingReadCloser{Reader: bytes.NewReader(nil)}
	_, err := getCParams(&goavpipe.XcParams{
		Vertical:           1,
		VerticalData:       []byte{1, 2, 3, 4},
		VerticalDataReader: r,
	})
	require.ErrorContains(t, err, "mutually exclusive")
}

func TestXcFiniReleasesVerticalDataReader(t *testing.T) {
	const handle = int32(-2_000_000_001)
	r := &trackingReadCloser{Reader: bytes.NewReader(nil)}
	readerHandle := newVerticalDataReaderHandle(r)
	putXCJob(handle, &xcJob{verticalDataReader: readerHandle})
	t.Cleanup(func() {
		if job, ok := takeXCJob(handle); ok && job.verticalDataReader != nil {
			_ = job.verticalDataReader.release()
		}
	})

	require.NoError(t, XcFini(handle))
	require.Equal(t, 1, r.closeCount)
}

func TestXcInitRejectsVerticalDataReaderInRawCopyModeAndClosesIt(t *testing.T) {
	r := &trackingReadCloser{Reader: bytes.NewReader(nil)}
	_, err := XcInit(&goavpipe.XcParams{
		VerticalDataReader: r,
		InputCfg: goavpipe.InputConfig{
			CopyMode: goavpipe.CopyModeRawOnly,
		},
	})

	require.Error(t, err)
	require.Equal(t, 1, r.closeCount)
}

type trackingReadCloser struct {
	io.Reader
	closeCount int
}

func (r *trackingReadCloser) Close() error {
	r.closeCount++
	return nil
}
