package mpegts

import (
	"context"
	"errors"
	"testing"

	elverrors "github.com/eluv-io/errors-go"
	"github.com/stretchr/testify/require"
)

func TestBypassProcessorStatusReturnsOutputCloseError(t *testing.T) {
	closeErr := errors.New("output close failed")
	bp := &BypassProcessor{outputCloseErr: closeErr}

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
		netReader:      &NetReader{ctx: ctx},
		outputCloseErr: closeErr,
	}

	running, err := bp.Status()
	require.False(t, running)
	require.ErrorIs(t, err, readErr)
	closeErrField, ok := elverrors.GetField(err, "output_close_error")
	require.True(t, ok)
	require.Equal(t, closeErr.Error(), closeErrField)
}
