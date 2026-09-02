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

	require.Same(t, inputOpener, goavpipe.GetURLInputOpener(url))
	require.Same(t, outputOpener, goavpipe.GetURLOutputOpener(url))
	require.NoError(t, XcFini(handle))

	xcJobsMu.Lock()
	_, jobExists := xcJobs[handle]
	xcJobsMu.Unlock()
	require.False(t, jobExists)
	require.Nil(t, goavpipe.GetURLInputOpener(url))
	require.Nil(t, goavpipe.GetURLOutputOpener(url))
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

	require.Nil(t, goavpipe.GetURLInputOpener(url))
	require.Nil(t, goavpipe.GetURLOutputOpener(url))

	xcJobsMu.Lock()
	jobsAfter := len(xcJobs)
	xcJobsMu.Unlock()
	require.Equal(t, jobsBefore, jobsAfter)
}

func TestTruncateToRequestedSize(t *testing.T) {
	t.Run("shorter than sz returned as-is", func(t *testing.T) {
		data := []byte{1, 2, 3}
		require.Equal(t, data, truncateToRequestedSize(data, 10))
	})

	t.Run("exactly sz returned as-is", func(t *testing.T) {
		data := []byte{1, 2, 3}
		require.Equal(t, data, truncateToRequestedSize(data, 3))
	})

	t.Run("longer than sz truncated", func(t *testing.T) {
		data := []byte{1, 2, 3, 4, 5}
		require.Equal(t, []byte{1, 2, 3}, truncateToRequestedSize(data, 3))
	})

	t.Run("zero sz truncates to empty", func(t *testing.T) {
		data := []byte{1, 2, 3}
		require.Empty(t, truncateToRequestedSize(data, 0))
	})
}

type xcJobTestInputOpener struct{}

func (*xcJobTestInputOpener) Open(_ int64, _ string) (goavpipe.InputHandler, error) {
	return nil, errors.New("unexpected input open")
}

type xcJobTestOutputOpener struct{}

func (*xcJobTestOutputOpener) Open(_, _ int64, _, _ int, _ int64, _ goavpipe.AVType) (goavpipe.OutputHandler, error) {
	return nil, errors.New("unexpected output open")
}
