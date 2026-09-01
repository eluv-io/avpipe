package xc_test

import (
	"errors"
	"io"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/stretchr/testify/require"
)

func TestXcRunRawOnlyReturnsTerminalErrorAndClosesInput(t *testing.T) {
	terminalErr := errors.New("reader failed")
	input := &rawOnlyTestInput{}
	processor := &rawOnlyTestProcessor{
		params:    &goavpipe.XcParams{Url: "test://raw-only-terminal-error"},
		statusErr: terminalErr,
	}
	handle, outputOpenerBefore := initRawOnlyTest(t, processor, input)

	err := avpipe.XcRun(handle)

	require.ErrorIs(t, err, terminalErr)
	require.True(t, processor.waited)
	require.Equal(t, 1, input.closes)
	assertRawOnlyTestCleanedUp(t, handle, processor.fd, outputOpenerBefore)
}

func TestXcRunRawOnlyClosesInputWhenStartFails(t *testing.T) {
	startErr := errors.New("start failed")
	input := &rawOnlyTestInput{}
	processor := &rawOnlyTestProcessor{
		params:   &goavpipe.XcParams{Url: "test://raw-only-start-error"},
		startErr: startErr,
	}
	handle, outputOpenerBefore := initRawOnlyTest(t, processor, input)

	err := avpipe.XcRun(handle)

	require.ErrorIs(t, err, startErr)
	require.False(t, processor.waited)
	require.Equal(t, 1, input.closes)
	assertRawOnlyTestCleanedUp(t, handle, processor.fd, outputOpenerBefore)
}

func TestXcRunRawOnlyReturnsInputCloseError(t *testing.T) {
	closeErr := errors.New("input close failed")
	input := &rawOnlyTestInput{closeErr: closeErr}
	processor := &rawOnlyTestProcessor{
		params: &goavpipe.XcParams{Url: "test://raw-only-close-error"},
	}
	handle, outputOpenerBefore := initRawOnlyTest(t, processor, input)

	err := avpipe.XcRun(handle)

	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to close raw-only input")
	require.Equal(t, 1, input.closes)
	assertRawOnlyTestCleanedUp(t, handle, processor.fd, outputOpenerBefore)
}

func initRawOnlyTest(t *testing.T, processor *rawOnlyTestProcessor, input *rawOnlyTestInput) (int32, goavpipe.OutputOpener) {
	t.Helper()
	outputOpenerBefore := goavpipe.GetOutputOpenerByHandler(-1)

	url := processor.params.Url
	inSet, outSet := goavpipe.InitUrlIOHandlerIfNotPresent(
		url,
		&rawOnlyTestInputOpener{input: input},
		rawOnlyTestOutputOpener{},
	)
	require.True(t, inSet)
	require.True(t, outSet)
	t.Cleanup(func() {
		goavpipe.Globals.RemoveURLHandlers(url)
	})

	return goavpipe.Globals.InitBypassProcessor(processor), outputOpenerBefore
}

func assertRawOnlyTestCleanedUp(t *testing.T, handle int32, fd int64, outputOpenerBefore goavpipe.OutputOpener) {
	t.Helper()

	_, processorExists := goavpipe.Globals.GetBypassProcessor(handle)
	require.False(t, processorExists)
	_, inputHandlerExists := goavpipe.Globals.GetCIOHandler(fd)
	require.False(t, inputHandlerExists)
	require.True(t, outputOpenerBefore == goavpipe.GetOutputOpenerByHandler(fd))
}

type rawOnlyTestProcessor struct {
	params    *goavpipe.XcParams
	startErr  error
	statusErr error
	fd        int64
	waited    bool
}

func (p *rawOnlyTestProcessor) Start(fd int64) error {
	p.fd = fd
	return p.startErr
}

func (p *rawOnlyTestProcessor) Cancel() {}

func (p *rawOnlyTestProcessor) Status() (bool, error) {
	return false, p.statusErr
}

func (p *rawOnlyTestProcessor) Wait() {
	p.waited = true
}

func (p *rawOnlyTestProcessor) XcParams() *goavpipe.XcParams {
	return p.params
}

type rawOnlyTestInputOpener struct {
	input *rawOnlyTestInput
}

func (o *rawOnlyTestInputOpener) Open(_ int64, _ string) (goavpipe.InputHandler, error) {
	return o.input, nil
}

type rawOnlyTestInput struct {
	closes   int
	closeErr error
}

func (i *rawOnlyTestInput) Read(_ []byte) (int, error) {
	return 0, io.EOF
}

func (i *rawOnlyTestInput) Seek(_ int64, _ int) (int64, error) {
	return 0, nil
}

func (i *rawOnlyTestInput) Close() error {
	i.closes++
	return i.closeErr
}

func (i *rawOnlyTestInput) Size() int64 {
	return 0
}

func (i *rawOnlyTestInput) Stat(_ int, _ goavpipe.AVStatType, _ interface{}) error {
	return nil
}

type rawOnlyTestOutputOpener struct{}

func (rawOnlyTestOutputOpener) Open(_, _ int64, _, _ int, _ int64, _ goavpipe.AVType) (goavpipe.OutputHandler, error) {
	return nil, errors.New("unexpected output open")
}
