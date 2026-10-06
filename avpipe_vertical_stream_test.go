package avpipe_test

// End-to-end tests for the streaming vertical crop data source (VerticalDataReader).
//
// The already-tested buffer source (VerticalData) is the oracle: feeding the same
// per-frame crop values through a pipe must produce byte-identical output. That
// pins down the whole path - one record consumed per decoded frame, the C->Go
// read callback, and EOF reusing the last value - without needing to analyse
// pixels. Whether the crop lands where the data says is covered at the filter
// level by libavpipe/test/test_vertical_crop.c.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/require"
)

const (
	// 2s of bbb at 30fps (timebase 1/30000) - 60 output frames, a few more decoded
	// past the duration to absorb reordering.
	verticalStreamDurationTs = 60000
	verticalStreamFrames     = 60

	// Denominator of a crop-centre value (VERTICAL_DATA_SCALE in avpipe_utils.h).
	verticalDataScale = 10000

	// Output size: setFastEncodeParams encodes at 180p, and crop_calc_width(180)
	// is 180*9/16 = 101.25 -> 101 -> rounded up to even.
	verticalStreamEncHeight = 180
	verticalStreamCropWidth = 102
)

// verticalSweep returns n crop-centre values sweeping left edge -> right edge,
// so every frame gets a distinct crop x and any off-by-one in record cadence
// shows up as a different output.
func verticalSweep(n int) []uint32 {
	vals := make([]uint32, n)
	for i := range vals {
		vals[i] = uint32(uint64(i) * verticalDataScale / uint64(n-1))
	}
	return vals
}

func encodeVerticalData(vals []uint32) []byte {
	buf := make([]byte, 4*len(vals))
	for i, v := range vals {
		binary.LittleEndian.PutUint32(buf[4*i:], v)
	}
	return buf
}

// streamVerticalData feeds vals through a pipe one record at a time, as a live
// producer would. io.Pipe is synchronous, so each write blocks until the
// transcoder's read callback consumes it. Closing the writer afterwards gives the
// reader EOF; closeErr, if set, is delivered to the reader instead of EOF.
// The returned done channel closes when the producer has exited.
func streamVerticalData(vals []uint32, pace time.Duration, closeErr error) (io.ReadCloser, <-chan struct{}) {
	pr, pw := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		var rec [4]byte
		for _, v := range vals {
			binary.LittleEndian.PutUint32(rec[:], v)
			if _, err := pw.Write(rec[:]); err != nil {
				return // reader released early (job ended or was cancelled)
			}
			if pace > 0 {
				time.Sleep(pace)
			}
		}
		if closeErr != nil {
			_ = pw.CloseWithError(closeErr)
		} else {
			_ = pw.Close()
		}
	}()
	return pr, done
}

// feedFIFO writes data to a named pipe one 4-byte record per pace, as a crop
// tracker would. Opening a FIFO for writing blocks until the reader opens its
// end, and a write after the reader has closed fails with EPIPE, which ends the
// producer. If nothing ever opened the read side, the cleanup does so itself to
// release the producer.
func feedFIFO(t *testing.T, fifo string, data []byte, pace time.Duration) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer w.Close()
		for i := 0; i+4 <= len(data); i += 4 {
			if _, err := w.Write(data[i : i+4]); err != nil {
				return
			}
			time.Sleep(pace)
		}
	}()
	t.Cleanup(func() {
		if r, err := os.OpenFile(fifo, os.O_RDONLY|syscall.O_NONBLOCK, 0); err == nil {
			r.Close()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("FIFO producer did not exit")
		}
	})
}

func verticalStreamParams(url string) *goavpipe.XcParams {
	params := &goavpipe.XcParams{
		Format:             "fmp4-segment",
		DurationTs:         verticalStreamDurationTs,
		VideoBitrate:       2560000,
		VideoSegDurationTs: 60000,
		Ecodec:             h264Codec,
		EncHeight:          720,
		EncWidth:           1280,
		XcType:             goavpipe.XcVideo,
		StreamId:           -1,
		Url:                url,
		DebugFrameLevel:    debugFrameLevel,
		Vertical:           1,
	}
	// Always use the fast encode settings, not just under -short: the byte-identity
	// oracle needs a deterministic encode, and the full-quality 720p libx264 encode
	// is not reproducible run to run (thread timing), whereas 320x180/ultrafast is.
	// Resolution and quality don't matter here - only how the crop values arrive.
	setFastEncodeParams(params, true)
	return params
}

// outputHashes returns sha256 per file in dir, keyed by file name.
func outputHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	hashes := map[string]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(path.Join(dir, e.Name()))
		require.NoError(t, err)
		sum := sha256.Sum256(data)
		hashes[e.Name()] = hex.EncodeToString(sum[:])
	}
	require.NotEmpty(t, hashes, "no output files in %s", dir)
	return hashes
}

func requireSameOutput(t *testing.T, wantDir, gotDir string) {
	t.Helper()
	want := outputHashes(t, wantDir)
	got := outputHashes(t, gotDir)
	require.Equal(t, len(want), len(got), "different number of output files: %s vs %s", wantDir, gotDir)
	for name, h := range want {
		require.Contains(t, got, name, "missing output file %s in %s", name, gotDir)
		require.Equal(t, h, got[name], "output file %s differs between %s and %s", name, wantDir, gotDir)
	}
}

func requireDifferentOutput(t *testing.T, aDir, bDir string) {
	t.Helper()
	a := outputHashes(t, aDir)
	b := outputHashes(t, bDir)
	for name, h := range a {
		if other, ok := b[name]; ok && other != h {
			return
		}
	}
	t.Fatalf("outputs in %s and %s are identical - the crop data had no effect, so equivalence checks are meaningless", aDir, bDir)
}

// requireVideoSize probes the first video segment in an output directory.
func requireVideoSize(t *testing.T, outDir string, width, height int) {
	t.Helper()
	segs, err := filepath.Glob(filepath.Join(outDir, "vsegment-*.mp4"))
	require.NoError(t, err)
	require.NotEmpty(t, segs, "no video segment in %s", outDir)
	// The same handlers boilerplate() installs for in-process probes.
	goavpipe.InitIOHandler(
		&xc.FileInputOpener{URL: segs[0], Stats: &statsInfo},
		&xc.FileOutputOpener{Dir: outDir, Stats: &statsInfo})
	info, err := avpipe.Probe(&goavpipe.XcParams{Url: segs[0], Seekable: true})
	require.NoError(t, err)
	require.NotEmpty(t, info.Streams)
	require.Equal(t, width, info.Streams[0].Width, "output width")
	require.Equal(t, height, info.Streams[0].Height, "output height")
}

// verticalBufferOracle transcodes sweep through the VerticalData buffer path and
// returns its output directory. First it proves the encode is deterministic (a
// second identical run must match) and that the data has an effect (a run with
// the crop held at centre must differ) - otherwise an equivalence check against
// the oracle could pass vacuously, or a mismatch would be ambiguous.
func verticalBufferOracle(t *testing.T, url, base string, sweep []uint32) string {
	t.Helper()
	bufferDir := path.Join(base, "buffer")
	params := verticalStreamParams(url)
	params.VerticalData = encodeVerticalData(sweep)
	xcTest(t, bufferDir, params, nil, true)

	bufferDir2 := path.Join(base, "buffer2")
	params = verticalStreamParams(url)
	params.VerticalData = encodeVerticalData(sweep)
	xcTest(t, bufferDir2, params, nil, true)
	requireSameOutput(t, bufferDir, bufferDir2)

	holdDir := path.Join(base, "hold")
	params = verticalStreamParams(url)
	hold := make([]uint32, len(sweep))
	for i := range hold {
		hold[i] = verticalDataScale / 2
	}
	params.VerticalData = encodeVerticalData(hold)
	xcTest(t, holdDir, params, nil, true)
	requireDifferentOutput(t, bufferDir, holdDir)

	return bufferDir
}

func TestVerticalStreamMatchesBufferEndToEnd(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	base := path.Join(baseOutPath, fn())

	// More records than frames get decoded: the buffer path indexes by frame and
	// ignores the tail, the stream path consumes one per decoded frame and leaves
	// the rest unread. Both must land on the same values.
	sweep := verticalSweep(verticalStreamFrames + 30)
	bufferDir := verticalBufferOracle(t, url, base, sweep)

	streamDir := path.Join(base, "stream")
	params := verticalStreamParams(url)
	reader, done := streamVerticalData(sweep, 2*time.Millisecond, nil)
	params.VerticalDataReader = reader
	xcTest(t, streamDir, params, nil, true)
	<-done

	requireSameOutput(t, bufferDir, streamDir)
}

func TestVerticalStreamFIFOMatchesBuffer(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	base := path.Join(baseOutPath, fn())
	setupOutDir(t, base) // a FIFO left behind by an earlier run would fail Mkfifo

	sweep := verticalSweep(verticalStreamFrames + 30)
	bufferDir := verticalBufferOracle(t, url, base, sweep)

	// A real named pipe, as a crop tracker feeds elvxc --vertical-data. Unlike
	// io.Pipe this goes through the kernel's FIFO semantics: the open rendezvous,
	// 4-byte reads on an *os.File, and EPIPE ending the producer once the
	// transcoder has closed the reader.
	fifo := path.Join(base, "crop.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	feedFIFO(t, fifo, encodeVerticalData(sweep), 10*time.Millisecond)
	reader, err := os.Open(fifo) // blocks until the producer has opened its end
	require.NoError(t, err)

	fifoDir := path.Join(base, "fifo")
	params := verticalStreamParams(url)
	params.VerticalDataReader = reader
	xcTest(t, fifoDir, params, nil, true)

	requireSameOutput(t, bufferDir, fifoDir)
	requireVideoSize(t, fifoDir, verticalStreamCropWidth, verticalStreamEncHeight)
}

func TestVerticalStreamEOFReusesLastValue(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	base := path.Join(baseOutPath, fn())

	// Only a third of the frames get a record. The buffer path clamps later
	// frames to its last entry; the stream path must hold its last value after
	// EOF. Same crop trajectory either way.
	partial := verticalSweep(verticalStreamFrames / 3)

	bufferDir := path.Join(base, "buffer")
	params := verticalStreamParams(url)
	params.VerticalData = encodeVerticalData(partial)
	xcTest(t, bufferDir, params, nil, true)

	streamDir := path.Join(base, "stream")
	params = verticalStreamParams(url)
	reader, done := streamVerticalData(partial, 0, nil)
	params.VerticalDataReader = reader
	xcTest(t, streamDir, params, nil, true)
	<-done

	requireSameOutput(t, bufferDir, streamDir)
}

func TestVerticalDataBufferMustBeWholeRecords(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	outputDir := path.Join(baseOutPath, fn())

	// 6 bytes is not a whole number of records: check_params rejects the buffer
	// before anything is decoded. (A stream can only fail at the bad record.)
	params := verticalStreamParams(url)
	params.VerticalData = encodeVerticalData(verticalSweep(2))[:6]

	boilerplate(t, outputDir, url)
	err := avpipe.Xc(params)
	require.ErrorIs(t, err, avpipe.EAV_PARAM,
		"a vertical data buffer that is not 4-byte aligned must be rejected up front")
}

func TestVerticalStreamReadErrorFailsTranscode(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	outputDir := path.Join(baseOutPath, fn())

	errProducer := errors.New("crop tracker died")
	params := verticalStreamParams(url)
	reader, done := streamVerticalData(verticalSweep(10), 0, errProducer)
	params.VerticalDataReader = reader

	boilerplate(t, outputDir, url)
	err := avpipe.Xc(params)
	<-done

	require.ErrorIs(t, err, avpipe.EAV_READ_INPUT,
		"a mid-stream read error must fail the transcode, not silently continue")
}

func TestVerticalStreamEOFBeforeFirstValueFailsTranscode(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	outputDir := path.Join(baseOutPath, fn())

	// A source that closes without ever producing a record: there is no value to
	// hold, so the transcode must fail rather than crop at the filter's placeholder.
	params := verticalStreamParams(url)
	reader, done := streamVerticalData(nil, 0, nil)
	params.VerticalDataReader = reader

	boilerplate(t, outputDir, url)
	err := avpipe.Xc(params)
	<-done

	require.ErrorIs(t, err, avpipe.EAV_READ_INPUT,
		"an empty vertical data source must fail the transcode")
}

// firstReadSignal wraps a reader and closes ready on the first Read call, so a
// test can wait until the transcoder is actually blocked in the read callback.
type firstReadSignal struct {
	io.ReadCloser
	once  sync.Once
	ready chan struct{}
}

func (r *firstReadSignal) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.ready) })
	return r.ReadCloser.Read(p)
}

func TestVerticalStreamCancelUnblocksBlockedReader(t *testing.T) {
	url := videoBigBuckBunnyPath
	checkFileExists(t, url)
	outputDir := path.Join(baseOutPath, fn())

	// A producer that never writes: the first frame's read blocks forever unless
	// cancellation closes the reader out from under it.
	pr, pw := io.Pipe()
	defer pw.Close()
	reader := &firstReadSignal{ReadCloser: pr, ready: make(chan struct{})}

	params := verticalStreamParams(url)
	params.VerticalDataReader = reader

	boilerplate(t, outputDir, url)
	handle, err := avpipe.XcInit(params)
	require.NoError(t, err)

	runErr := make(chan error, 1)
	go func() { runErr <- avpipe.XcRun(handle) }()

	select {
	case <-reader.ready:
	case <-time.After(30 * time.Second):
		t.Fatal("transcoder never asked for vertical data")
	}

	require.NoError(t, avpipe.XcCancel(handle))

	select {
	case err := <-runErr:
		require.Error(t, err)
		require.True(t, errors.Is(err, avpipe.EAV_CANCELLED) || errors.Is(err, avpipe.EAV_READ_INPUT),
			"unexpected error after cancel: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("XcRun did not return after cancel - reader close did not unblock the read")
	}

	require.NoError(t, avpipe.XcFini(handle))
}
