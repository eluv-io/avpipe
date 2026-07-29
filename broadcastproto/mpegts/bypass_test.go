package mpegts

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	elverrors "github.com/eluv-io/errors-go"
)

func TestBypassProcessorStatusReturnsOutputCloseError(t *testing.T) {
	closeErr := errors.New("output close failed")
	bp := &BypassProcessor{}
	bp.outputCloseErr.Store(&errWrapper{err: closeErr})

	running, err := bp.Status()
	require.False(t, running)
	require.ErrorIs(t, err, closeErr)
}

func TestBypassProcessorStatusPreservesReaderAndOutputCloseErrors(t *testing.T) {
	readErr := errors.New("reader failed")
	closeErr := errors.New("output close failed")
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(readErr)

	bp := &BypassProcessor{
		netReader: &NetReader{ctx: ctx},
	}
	bp.outputCloseErr.Store(&errWrapper{err: closeErr})

	running, err := bp.Status()
	require.False(t, running)
	require.ErrorIs(t, err.(*elverrors.ErrorList).Errors[0], readErr)
	require.ErrorIs(t, err.(*elverrors.ErrorList).Errors[1], closeErr)
}
