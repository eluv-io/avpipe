package mp4e

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSegment(t *testing.T) {
	f := "testdata/vfsegment.mp4"
	rd, err := os.Open(f)
	require.NoError(t, err)

	_, stats, err := ParseFmp4(rd)
	require.NoError(t, err)
	require.Empty(t, stats.Errors)
}

// TestCheckSegments validates all the MP4 segments in a directory
// TODO validate the segments together as one file
func TestCheckSegments(t *testing.T) {
	directory := "testdata"

	// Iterate over all files in the directory
	files, err := os.ReadDir(directory)
	require.NoError(t, err)
	for _, file := range files {
		if file.IsDir() || strings.LastIndex(file.Name(), ".m") != len(file.Name())-4 {
			continue
		}
		//println("\nChecking", file.Name())
		f, err2 := os.Open(directory + "/" + file.Name())
		require.NoError(t, err2)

		fstat, err2 := f.Stat()
		require.NoError(t, err2)

		_, stats, err2 := ParseFmp4(f)
		require.NoError(t, err2)
		require.EqualValues(t, fstat.Size(), stats.Size)
		require.Empty(t, stats.Errors)
	}
}
