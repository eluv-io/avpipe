package avpipe_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
	"github.com/stretchr/testify/require"
)

func TestUniqfeedDemoSmoke(t *testing.T) {
	if os.Getenv("AVPIPE_RUN_UNIQFEED_SMOKE") != "1" {
		t.Skip("set AVPIPE_RUN_UNIQFEED_SMOKE=1 to run the uniqfeed smoke test")
	}

	demoDir := os.Getenv("AVPIPE_UNIQFEED_DEMO_DIR")
	if demoDir == "" {
		demoDir = filepath.Clean(filepath.Join("..", "tnt-uniqfeed"))
	}

	demoScript := filepath.Join(demoDir, "demo_uniqfeed.sh")
	if !fileExist(demoScript) {
		t.Skipf("uniqfeed demo script missing: %s", demoScript)
	}

	ffmpegBin := os.Getenv("AVPIPE_UNIQFEED_FFMPEG_BIN")
	if ffmpegBin == "" {
		homeDir, err := os.UserHomeDir()
		require.NoError(t, err)
		ffmpegBin = filepath.Join(homeDir, ".local", "bin", "ffmpeg")
	}
	if !fileExist(ffmpegBin) {
		t.Skipf("uniqfeed ffmpeg binary missing: %s", ffmpegBin)
	}

	homeDir, err := os.UserHomeDir()
	require.NoError(t, err)
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "uniqfeed-demo.mp4")

	ldLib := strings.Join([]string{
		filepath.Join(homeDir, ".local", "lib"),
		filepath.Join(homeDir, ".local", "lib", "uf"),
		filepath.Join(homeDir, ".local", "lib", "3rdparty"),
		os.Getenv("LD_LIBRARY_PATH"),
	}, ":")

	cmd := exec.Command(demoScript, "-d", "1", "-o", outFile)
	cmd.Dir = demoDir
	cmd.Env = append(os.Environ(),
		"FFMPEG_BIN="+ffmpegBin,
		"LD_LIBRARY_PATH="+ldLib,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	outText := string(output)
	require.NotContains(t, outText, "uniqFEED failed", "demo fell back to passthrough:\n%s", outText)
	require.NotContains(t, outText, "passing through", "demo fell back to passthrough:\n%s", outText)
	require.Contains(t, outText, "uniqfeed filter found in FFmpeg")
	require.Contains(t, outText, "Demo complete!")
	require.FileExists(t, outFile)

	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: outFile}, &concurrentOutputOpener{dir: filepath.Join(outDir, "probe")})
	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: outFile, Seekable: true})
	require.NoError(t, err)

	video := probe.StreamByCodecType("video")
	require.NotNil(t, video)
	require.Equal(t, 1920, video.Width)
	require.Equal(t, 1080, video.Height)
	require.Greater(t, video.NBFrames, int64(0))
}
