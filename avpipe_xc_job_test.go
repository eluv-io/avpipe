package avpipe

import (
	"errors"
	"testing"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/require"
)

func TestXcFiniRemovesJobAndURLHandlers(t *testing.T) {
	const (
		handle = int32(-2_000_000_000)
		url    = "test://xc-fini/removes-job"
	)

	inputOpener := &xcJobTestInputOpener{}
	outputOpener := &xcJobTestOutputOpener{}
	goavpipe.InitUrlIOHandler(url, inputOpener, outputOpener)
	t.Cleanup(func() {
		goavpipe.Globals.RemoveURLHandlers(url)
		_, _ = takeXCJob(handle)
	})

	putXCJob(handle, &xcJob{url: url})

	require.Same(t, inputOpener, goavpipe.GetInputOpener(url))
	require.Same(t, outputOpener, goavpipe.GetOutputOpener(url))
	require.NoError(t, XcFini(handle))

	xcJobsMu.Lock()
	_, jobExists := xcJobs[handle]
	xcJobsMu.Unlock()
	require.False(t, jobExists)
	require.NotSame(t, inputOpener, goavpipe.GetInputOpener(url))
	require.NotSame(t, outputOpener, goavpipe.GetOutputOpener(url))
	require.ErrorIs(t, XcFini(handle), EAV_BAD_HANDLE)
}

func TestXcInitFailureRollsBackURLHandlersAndDoesNotCreateJob(t *testing.T) {
	const url = "test://xc-init/failure-rollback"

	inputOpener := &xcJobTestInputOpener{}
	outputOpener := &xcJobTestOutputOpener{}
	goavpipe.InitUrlIOHandler(url, inputOpener, outputOpener)
	t.Cleanup(func() {
		goavpipe.Globals.RemoveURLHandlers(url)
	})

	xcJobsMu.Lock()
	jobsBefore := len(xcJobs)
	xcJobsMu.Unlock()

	params := &goavpipe.XcParams{
		Url:        url,
		AudioIndex: make([]int32, MaxAudioMux+1),
	}
	handle, err := XcInit(params)

	require.Error(t, err)
	require.Equal(t, int32(-1), handle)
	require.NotSame(t, inputOpener, goavpipe.GetInputOpener(url))
	require.NotSame(t, outputOpener, goavpipe.GetOutputOpener(url))

	xcJobsMu.Lock()
	jobsAfter := len(xcJobs)
	xcJobsMu.Unlock()
	require.Equal(t, jobsBefore, jobsAfter)
}

type xcJobTestInputOpener struct{}

func (*xcJobTestInputOpener) Open(_ int64, _ string) (goavpipe.InputHandler, error) {
	return nil, errors.New("unexpected input open")
}

type xcJobTestOutputOpener struct{}

func (*xcJobTestOutputOpener) Open(_, _ int64, _, _ int, _ int64, _ goavpipe.AVType) (goavpipe.OutputHandler, error) {
	return nil, errors.New("unexpected output open")
}
