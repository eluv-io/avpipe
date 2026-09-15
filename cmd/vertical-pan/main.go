// vertical-pan drives the vertical (9:16) crop over a generated per-frame pan
// pattern, so the result can be eyeballed on any source.
//
// The unit test (libavpipe/test/test_vertical_crop.c) asserts the crop tracks
// vertical_data numerically on a synthetic gradient. This utility is the visual
// counterpart: point it at real footage, watch the window move, and confirm it
// looks the way the pattern says it should.
//
//	go run ./cmd/vertical-pan -source media/foo.mp4 -pattern pingpong -frames 180
//
// Output length is trimmed to the pattern length so playback ends when the
// pattern does.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/xc"
)

const appName = "vertical-pan"

// vertical_data values are the crop-window centre as a fraction of the scaled
// frame width. vertical_data_crop_x() infers the divisor from the value's digit
// count, so 3-digit values are always read as 0.<digits> - i.e. divided by 1000.
// Staying in [100,999] keeps that unambiguous.
//
// safeLo/safeHi bound the range where the window is fully inside the frame for a
// 9:16 crop: half the crop width is (9/16)/2 = 0.28125 of the height, which for a
// 16:9 source is 0.28125*(9/16) = 0.158 of the width. Outside that the crop
// clamps to the frame edge and stops tracking.
const (
	patternLo = 160 // ~0.160 - just inside the left clamp
	patternHi = 840 // ~0.840 - just inside the right clamp
	fullLo    = 100 // 0.100 - deliberately inside the left clamp region
	fullHi    = 999 // 0.999 - deliberately inside the right clamp region
)

// shape returns a 0..1 position for frame n of total, per the named pattern.
func shape(name string, n, total int) (float64, error) {
	t := float64(n) / float64(total)
	switch name {
	case "pingpong":
		// Slow sweep up, three fast ping-pongs, a hold at mid, then sweep down.
		// Same shape the unit test uses.
		switch {
		case t < 0.25:
			return t / 0.25, nil
		case t < 0.55:
			return math.Abs(1 - 2*math.Mod((t-0.25)/0.30*3, 1)), nil
		case t < 0.70:
			return 0.5, nil
		default:
			return 1 - (t-0.70)/0.30, nil
		}
	case "sweep":
		return t, nil
	case "hold":
		return 0.5, nil
	case "edges":
		// Slam between the extremes, pausing at each - drives into the clamp
		// regions the unit test avoids, so you can see where tracking stops.
		if math.Mod(t*6, 2) < 1 {
			return 0, nil
		}
		return 1, nil
	}
	return 0, fmt.Errorf("unknown pattern %q (want pingpong, sweep, hold or edges)", name)
}

// buildPattern returns the vertical_data buffer: one little-endian uint32 per frame.
func buildPattern(name string, frames int) ([]byte, []uint32, error) {
	lo, hi := patternLo, patternHi
	if name == "edges" {
		lo, hi = fullLo, fullHi
	}
	buf := make([]byte, frames*4)
	vals := make([]uint32, frames)
	for n := 0; n < frames; n++ {
		c, err := shape(name, n, frames)
		if err != nil {
			return nil, nil, err
		}
		v := uint32(lo + int(math.Round(float64(hi-lo)*c)))
		vals[n] = v
		binary.LittleEndian.PutUint32(buf[n*4:], v)
	}
	return buf, vals, nil
}

// panOutputOpener writes the whole-file "mp4" output to one predictably named
// file. xc.FileOutputOpener has no case for MP4Stream (it targets segmented
// formats), so it would hand libavformat an empty filename.
type panOutputOpener struct {
	path string
}

func (o *panOutputOpener) Open(_, _ int64, _, _ int, _ int64,
	outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	if outType != goavpipe.MP4Stream {
		return nil, fmt.Errorf("unexpected output type %s (want MP4Stream)", outType.Name())
	}
	f, err := os.OpenFile(o.path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}
	return &panOutput{file: f}, nil
}

type panOutput struct{ file *os.File }

func (o *panOutput) Write(buf []byte) (int, error) { return o.file.Write(buf) }
func (o *panOutput) Seek(off int64, whence int) (int64, error) {
	return o.file.Seek(off, whence)
}
func (o *panOutput) Close() error { return o.file.Close() }
func (o *panOutput) Stat(int, goavpipe.AVType, goavpipe.AVStatType, interface{}) error {
	return nil
}

// cropCalcWidth mirrors crop_calc_width() in avpipe_filters.c.
func cropCalcWidth(height int) int {
	w := height * 9 / 16
	if w%2 != 0 {
		w++
	}
	return w
}

// cropX mirrors vertical_data_crop_x() in avpipe_utils.c, for the report only.
func cropX(v uint32, scaledWidth, cropWidth int) int {
	centerX := 0
	if v > 0 {
		divisor := uint64(1)
		for divisor <= uint64(v) {
			divisor *= 10
		}
		centerX = int(uint64(v) * uint64(scaledWidth) / divisor)
	}
	x := centerX - cropWidth/2
	if x < 0 {
		x = 0
	}
	if maxX := scaledWidth - cropWidth; x > maxX {
		x = maxX
	}
	return x
}

func main() {
	var (
		source       = flag.String("source", "", "source media file (required)")
		pattern      = flag.String("pattern", "pingpong", "pan pattern: pingpong, sweep, hold, edges")
		frames       = flag.Int("frames", 180, "number of frames to generate and transcode")
		height       = flag.Int("height", 360, "output height; crop width is height*9/16")
		out          = flag.String("out", "", "output file path; its directory must exist (default: ./<source>-<pattern>.mp4)")
		writePattern = flag.String("write-pattern", "", "also write the raw vertical_data to this file (for exc -vertical-data)")
		showTable    = flag.Bool("table", false, "print the per-frame value/crop table")
	)
	flag.Parse()

	if err := run(*source, *pattern, *frames, *height, *out, *writePattern, *showTable); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", appName, err)
		os.Exit(1)
	}
}

func run(source, pattern string, frames, height int, outPath, writePattern string, showTable bool) error {
	if source == "" {
		flag.Usage()
		return fmt.Errorf("-source is required")
	}
	if frames <= 0 {
		return fmt.Errorf("-frames must be positive, got %d", frames)
	}
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("source: %w", err)
	}
	if outPath == "" {
		outPath = fmt.Sprintf("%s-%s.mp4",
			strings.TrimSuffix(filepath.Base(source), filepath.Ext(source)), pattern)
	}
	// Fail here rather than deep inside the transcode: the output file is not
	// opened until libavformat writes the header.
	if dir := filepath.Dir(outPath); dir != "." {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return fmt.Errorf("output directory %s does not exist", dir)
		}
	}

	data, vals, err := buildPattern(pattern, frames)
	if err != nil {
		return err
	}

	if writePattern != "" {
		if err := os.WriteFile(writePattern, data, 0644); err != nil {
			return fmt.Errorf("write pattern: %w", err)
		}
		fmt.Printf("pattern:  %s (%d frames, %d bytes)\n", writePattern, frames, len(data))
	}

	// Must be registered before Probe as well as Xc - both go through the handlers.
	goavpipe.InitIOHandler(&xc.FileInputOpener{URL: source}, &panOutputOpener{path: outPath})

	probe, err := avpipe.Probe(&goavpipe.XcParams{Url: source, Seekable: true})
	if err != nil {
		return fmt.Errorf("probe %s: %w", source, err)
	}
	var vs *goavpipe.StreamInfo
	for i := range probe.Streams {
		if probe.Streams[i].CodecType == "video" {
			vs = &probe.Streams[i]
			break
		}
	}
	if vs == nil {
		return fmt.Errorf("%s has no video stream", source)
	}

	// The crop window must fit inside the scaled frame.
	cropW := cropCalcWidth(height)
	scaledW := vs.Width * height / vs.Height
	if scaledW < cropW {
		return fmt.Errorf("source %dx%d scaled to height %d is %dpx wide, narrower than the "+
			"%dpx 9:16 crop - use a smaller -height or a wider source",
			vs.Width, vs.Height, height, scaledW, cropW)
	}

	// Trim the output to the pattern length: frames / fps seconds, in input ticks.
	fps := vs.AvgFrameRate
	if fps == nil || fps.Sign() == 0 {
		fps = vs.FrameRate
	}
	if fps == nil || fps.Sign() == 0 {
		return fmt.Errorf("%s reports no frame rate; cannot trim to %d frames", source, frames)
	}
	if vs.TimeBase == nil || vs.TimeBase.Sign() == 0 {
		return fmt.Errorf("%s reports no timebase", source)
	}
	// durationTs = frames / fps / timebase
	secs := new(big.Rat).Quo(new(big.Rat).SetInt64(int64(frames)), fps)
	durationTs, _ := new(big.Rat).Quo(secs, vs.TimeBase).Float64()

	fmt.Printf("source:   %s (%dx%d, %s fps, timebase %s)\n",
		source, vs.Width, vs.Height, fps.RatString(), vs.TimeBase.RatString())
	fmt.Printf("pattern:  %s, %d frames, values %d..%d\n", pattern, frames, minOf(vals), maxOf(vals))
	fmt.Printf("crop:     %dx%d window in a %dpx-wide scaled frame (x range 0..%d)\n",
		cropW, height, scaledW, scaledW-cropW)
	fmt.Printf("duration: %d ts\n", int64(durationTs))

	if showTable {
		printTable(vals, scaledW, cropW)
	}

	params := &goavpipe.XcParams{
		Url:                 source,
		BypassTranscoding:   false,
		Format:              "mp4", // single viewable file rather than segments
		XcType:              goavpipe.XcVideo,
		StartTimeTs:         0,
		DurationTs:          int64(durationTs),
		StartSegmentStr:     "1",
		SegDuration:         "30",
		Ecodec:              "libx264",
		CrfStr:              "18",
		EncHeight:           int32(height),
		EncWidth:            int32(scaledW), // overridden by the crop
		StreamId:            -1,
		SyncAudioToStreamId: -1,
		Vertical:            1,
		VerticalData:        data,
	}

	if err := avpipe.Xc(params); err != nil {
		return fmt.Errorf("transcode: %w", err)
	}

	fmt.Printf("\nwrote %s\n", outPath)
	return nil
}

func printTable(vals []uint32, scaledW, cropW int) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "\nframe\tvalue\tfraction\tcrop_x\twindow")
	for n, v := range vals {
		d := uint64(1)
		for d <= uint64(v) {
			d *= 10
		}
		x := cropX(v, scaledW, cropW)
		fmt.Fprintf(w, "%d\t%d\t%.3f\t%d\t[%d, %d)\n",
			n, v, float64(v)/float64(d), x, x, x+cropW)
	}
	_ = w.Flush()
}

func minOf(v []uint32) uint32 {
	m := v[0]
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}

func maxOf(v []uint32) uint32 {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}
