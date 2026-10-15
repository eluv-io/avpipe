package cmd

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestVerticalDataSourceFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "crop.bin")
	data := []byte{1, 0, 0, 0, 2, 0, 0, 0}
	require.NoError(t, os.WriteFile(file, data, 0o644))

	// A regular file is a shared read-only buffer: any thread count is fine.
	for _, threads := range []int32{1, 4} {
		buf, stream, err := verticalDataSource(file, threads)
		require.NoError(t, err)
		require.False(t, stream)
		require.Equal(t, data, buf)
	}
}

func TestVerticalDataSourceFIFO(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "crop.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	// No producer is attached: deciding must not open the FIFO, or this would block.
	buf, stream, err := verticalDataSource(fifo, 1)
	require.NoError(t, err)
	require.True(t, stream)
	require.Nil(t, buf)

	_, _, err = verticalDataSource(fifo, 2)
	require.ErrorContains(t, err, "exactly one transcoding thread")
}

func TestVerticalDataSourceMissing(t *testing.T) {
	_, _, err := verticalDataSource(filepath.Join(t.TempDir(), "missing.bin"), 1)
	require.Error(t, err)
}

// The gate is applied while flags are validated, before any output directory,
// handler or transcode exists, so the real command can be driven with no media.
func TestTranscodeRefusesStreamWithThreads(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "crop.fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))

	root := &cobra.Command{Use: "elvxc", SilenceUsage: true, SilenceErrors: true}
	require.NoError(t, InitTranscode(root))
	root.SetArgs([]string{"transcode", "-f", "input.mp4", "--xc-type", "video", "--video-seg-duration-ts", "60000",
		"--vertical", "1", "--vertical-data", fifo, "--threads", "2"})

	require.ErrorContains(t, root.Execute(), "exactly one transcoding thread")
}

func TestCopyPackagingMode(t *testing.T) {
	// "" is the flag's default: unset, so the transport picks its own default.
	for in, want := range map[string]transport.TsPackagingMode{
		"":       transport.UnknownPackagingMode,
		"raw_ts": transport.RawTs,
		"rtp_ts": transport.RtpTs,
		"ats_ts": transport.AtsTs,
	} {
		got, err := copyPackagingMode(in)
		require.NoError(t, err, "copy-packaging %q", in)
		require.Equal(t, want, got, "copy-packaging %q", in)
	}
	_, err := copyPackagingMode("mpegts")
	require.Error(t, err)
}
