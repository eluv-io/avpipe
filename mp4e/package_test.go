package mp4e_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"sync"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/mp4e"
)

const fragmentedMezFixture = "../cmd/mez-validator/testdata/video-mez-segment-1.mp4"

// --- minimal in-memory IO handlers used by these tests ----------------------

type fileInputOpener struct{ path string }

type fileInput struct{ f *os.File }

func (o *fileInputOpener) Open(_ int64, _ string) (goavpipe.InputHandler, error) {
	f, err := os.Open(o.path)
	if err != nil {
		return nil, err
	}
	return &fileInput{f: f}, nil
}

func (i *fileInput) Read(p []byte) (int, error) {
	n, err := i.f.Read(p)
	if err == io.EOF {
		return 0, nil // avpipe convention
	}
	return n, err
}
func (i *fileInput) Seek(offset int64, whence int) (int64, error) {
	return i.f.Seek(offset, whence)
}
func (i *fileInput) Close() error { return i.f.Close() }
func (i *fileInput) Size() int64 {
	st, err := i.f.Stat()
	if err != nil {
		return -1
	}
	return st.Size()
}
func (i *fileInput) Stat(int, goavpipe.AVStatType, interface{}) error { return nil }

// memOutputOpener captures every produced segment in-memory keyed by
// (outType, segIdx) so tests can assert structure without touching disk.
type memOutputOpener struct {
	mu   sync.Mutex
	outs map[string]*bytes.Buffer
	keys []string
}

func newMemOutputOpener() *memOutputOpener {
	return &memOutputOpener{outs: map[string]*bytes.Buffer{}}
}

func (o *memOutputOpener) key(outType goavpipe.AVType, segIdx int) string {
	return fmt.Sprintf("%s-%d", outType.Name(), segIdx)
}

func (o *memOutputOpener) Open(_, _ int64, _, segIdx int, _ int64,
	outType goavpipe.AVType) (goavpipe.OutputHandler, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	k := o.key(outType, segIdx)
	buf := &bytes.Buffer{}
	o.outs[k] = buf
	o.keys = append(o.keys, k)
	return &memOutput{buf: buf}, nil
}

func (o *memOutputOpener) get(outType goavpipe.AVType, segIdx int) *bytes.Buffer {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.outs[o.key(outType, segIdx)]
}

func (o *memOutputOpener) count(outType goavpipe.AVType) int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for _, k := range o.keys {
		if len(k) >= len(outType.Name()) && k[:len(outType.Name())] == outType.Name() {
			n++
		}
	}
	return n
}

type memOutput struct{ buf *bytes.Buffer }

func (m *memOutput) Write(p []byte) (int, error)            { return m.buf.Write(p) }
func (m *memOutput) Seek(int64, int) (int64, error)         { return 0, fmt.Errorf("seek unsupported") }
func (m *memOutput) Close() error                           { return nil }
func (m *memOutput) Stat(int, goavpipe.AVType, goavpipe.AVStatType, interface{}) error {
	return nil
}

func registerIO(t *testing.T, url, path string, out *memOutputOpener) {
	t.Helper()
	goavpipe.InitUrlIOHandler(url, &fileInputOpener{path: path}, out)
	t.Cleanup(func() { goavpipe.Globals.RemoveURLHandlers(url) })
}

// --- tests -------------------------------------------------------------------

// MakeMezPart with a fragmented input that contains a single ~30s mez segment.
// With SegDuration=30 we expect exactly one output mez segment back; with
// SegDuration=5 we expect multiple, broken at sync samples.
func TestMakeMezPart_Fragmented_ReturnsValidFmp4(t *testing.T) {
	out := newMemOutputOpener()
	url := "mp4e_test_mez_frag"
	registerIO(t, url, fragmentedMezFixture, out)

	err := mp4e.MakeMezPart(&goavpipe.XcParams{
		Url:         url,
		XcType:      goavpipe.XcVideo,
		SegDuration: "30",
		StreamId:    -1,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, out.count(goavpipe.FMP4VideoSegment), 1)

	// First segment must decode as fragmented mp4 and validate cleanly.
	seg1 := out.get(goavpipe.FMP4VideoSegment, 1)
	require.NotNil(t, seg1)
	_, info, vErr := mp4e.ValidateFmp4(bytes.NewReader(seg1.Bytes()))
	require.NoError(t, vErr)
	require.Empty(t, info.Errors, "validation errors: %v", info.Errors)
}

// MakeAbrSegments takes the 30s fragmented mez fixture and re-segments it into
// short ABR segments (~2s at 24000 timescale = 48000 ticks).
func TestMakeAbrSegments_ReturnsInitAndSegments(t *testing.T) {
	out := newMemOutputOpener()
	url := "mp4e_test_abr"
	registerIO(t, url, fragmentedMezFixture, out)

	err := mp4e.MakeAbrSegments(&goavpipe.XcParams{
		Url:                url,
		XcType:             goavpipe.XcVideo,
		VideoSegDurationTs: 48000,
		StreamId:           -1,
	})
	require.NoError(t, err)

	// One init + at least one media segment.
	require.Equal(t, 1, out.count(goavpipe.DASHVideoInit))
	require.GreaterOrEqual(t, out.count(goavpipe.DASHVideoSegment), 1)

	// Init must decode as an InitSegment (Moov + stsd).
	initBuf := out.get(goavpipe.DASHVideoInit, 0)
	require.NotNil(t, initBuf)
	initFile, err := mp4.DecodeFile(bytes.NewReader(initBuf.Bytes()))
	require.NoError(t, err)
	require.NotNil(t, initFile.Moov)

	// First media segment validates.
	seg1 := out.get(goavpipe.DASHVideoSegment, 1)
	require.NotNil(t, seg1)
	mf, err := mp4.DecodeFile(bytes.NewReader(seg1.Bytes()))
	require.NoError(t, err)
	require.True(t, mf.IsFragmented())
	require.NotEmpty(t, mf.Segments)
}

func TestMakeMezPart_RejectsAudioXcType(t *testing.T) {
	out := newMemOutputOpener()
	url := "mp4e_test_reject_audio"
	registerIO(t, url, fragmentedMezFixture, out)

	err := mp4e.MakeMezPart(&goavpipe.XcParams{
		Url:         url,
		XcType:      goavpipe.XcAudio,
		SegDuration: "30",
	})
	require.Error(t, err)
}

// TestIsMVHEVC_FalseOnPlainHEVC asserts that the fragmented mez fixture (a
// plain HEVC mez, not MV-HEVC) is not classified as MV-HEVC. We do not have a
// real MV-HEVC fixture committed, so only the negative case is covered here.
func TestIsMVHEVC_FalseOnPlainHEVC(t *testing.T) {
	f, err := os.Open(fragmentedMezFixture)
	require.NoError(t, err)
	defer f.Close()
	ok, err := mp4e.IsMVHEVC(f)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestIsMVHEVCUrl_UsesRegisteredOpener(t *testing.T) {
	out := newMemOutputOpener()
	url := "mp4e_test_ismvhevc"
	registerIO(t, url, fragmentedMezFixture, out)

	ok, err := mp4e.IsMVHEVCUrl(url)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestMakeAbrSegments_RequiresChunkDuration(t *testing.T) {
	out := newMemOutputOpener()
	url := "mp4e_test_abr_nodur"
	registerIO(t, url, fragmentedMezFixture, out)

	err := mp4e.MakeAbrSegments(&goavpipe.XcParams{
		Url:    url,
		XcType: goavpipe.XcVideo,
	})
	require.Error(t, err)
}
