package mp4e

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseSegment(t *testing.T) {
	//f := "testdata/vfsegment.mp4" // something wrong with this file
	f := "../cmd/mez-validator/testdata/video-mez-segment-1.mp4"
	rd, err := os.Open(f)
	require.NoError(t, err)

	_, stats, err := ValidateFmp4(rd)
	require.NoError(t, err)
	require.Empty(t, stats.Errors)
}

// TestCheckSegments validates all the fMP4 segments in a directory
// TODO validate the segments together as one file
func TestCheckSegments(t *testing.T) {
	directory := "../cmd/mez-validator/testdata"

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

		_, stats, err2 := ValidateFmp4(f)
		require.NoError(t, err2)
		require.EqualValues(t, fstat.Size(), stats.Size)
		require.Empty(t, stats.Errors)
	}
}

func TestIsSampleCountSequenceBad(t *testing.T) {
	tests := []struct {
		name     string
		a, b, c  uint64
		expected bool
	}{
		{"allEqual", 50, 50, 50, false},
		{"nextUnequal", 50, 50, 14, false},
		{"nextPartial", 50, 14, 36, false},
		{"prevPartial", 14, 36, 50, false},
		{"prevPrevPartial", 36, 50, 50, false},
		{"prevUnequal", 50, 48, 50, true},
		{"twoUnequal", 50, 48, 51, true},
		{"prevTwoUnequal", 48, 51, 50, true},
		{"first", 0, 0, 50, false},
		{"second", 0, 50, 50, false},
		{"firstUnequal", 0, 49, 50, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := isSampleCountSequenceBad(tc.a, tc.b, tc.c)
			require.Equal(t, tc.expected, result)
		})
	}
}
