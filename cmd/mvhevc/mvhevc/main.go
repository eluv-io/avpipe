package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/eluv-io/avpipe/mp4e/mvhevc"
	elog "github.com/eluv-io/log-go"
)

const appName = "mvhevc"

var usage = `%s handles MV-HEVC helper operations.

Subcommands:
  info [-idr] <input.mp4>
  add [-fps <rate>] [-spatial] <input.hevc|mp4> <output.mp4>
  fix <input.mp4> <output.mp4>

Usage of %s:
`

func main() {
	configureLogging()
	if err := run(os.Args, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func configureLogging() {
	elog.SetDefault(&elog.Config{
		Level:   "info",
		Handler: "console",
	})
}

func run(args []string, w io.Writer) error {
	if len(args) < 2 {
		printUsage()
		return fmt.Errorf("need subcommand: info, add, or fix")
	}

	switch args[1] {
	case "info":
		return runInfo(args[1:], w)
	case "add":
		return runAdd(args[1:])
	case "fix":
		return runFix(args[1:])
	default:
		printUsage()
		return fmt.Errorf("unknown subcommand: %s", args[1])
	}
}

func runInfo(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("info", flag.ContinueOnError)
	var opts mvhevc.InfoOptions
	fs.BoolVar(&opts.ShowIDR, "idr", false, "Show IDR (sync) frame positions")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, "%s info [-idr] <input.mp4>\n\n", appName)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("need input file")
	}

	return mvhevc.Info(fs.Arg(0), opts, w)
}

func runAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	var opts mvhevc.AddOptions
	var baseline uint
	var hfov uint
	fps := fpsFlag{value: &opts.FPS}
	fs.Var(&fps, "fps", "Frame rate (required for .hevc input, e.g., 24000/1001, 23.976, 24, 30, 60)")
	fs.BoolVar(&opts.Spatial, "spatial", false, "Add Apple spatial video metadata (vexu/hfov)")
	fs.UintVar(&baseline, "baseline", uint(mvhevc.DefaultBaselineUM), "Camera baseline in micrometers")
	fs.UintVar(&hfov, "hfov", uint(mvhevc.DefaultHFOV), "Horizontal FOV in 1/1000 degrees")
	fs.StringVar(&opts.HeroEye, "hero", "left", "Hero eye: left, right, or none")
	fs.BoolVar(&opts.Reversed, "reversed", false, "Eye views are reversed (right eye is base layer)")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, "%s add [-fps <rate>] [-spatial] <input.hevc|mp4> <output.mp4>\n\n", appName)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("need input and output files")
	}

	opts.BaselineUM = uint32(baseline)
	opts.HFOV = uint32(hfov)
	return mvhevc.Add(fs.Arg(0), fs.Arg(1), opts)
}

func runFix(args []string) error {
	fs := flag.NewFlagSet("fix", flag.ContinueOnError)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, "%s fix <input.mp4> <output.mp4>\n\n", appName)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("need input and output files")
	}

	return mvhevc.Fix(fs.Arg(0), fs.Arg(1))
}

func printUsage() {
	_, _ = fmt.Fprintf(os.Stderr, usage, appName, appName)
}

type fpsFlag struct {
	value *float64
}

func (f fpsFlag) String() string {
	if f.value == nil {
		return ""
	}
	return strconv.FormatFloat(*f.value, 'f', -1, 64)
}

func (f fpsFlag) Set(s string) error {
	fps, err := parseFPS(s)
	if err != nil {
		return err
	}
	*f.value = fps
	return nil
}

func parseFPS(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("fps must not be empty")
	}

	parts := strings.Split(s, "/")
	switch len(parts) {
	case 1:
		fps, err := strconv.ParseFloat(parts[0], 64)
		if err != nil {
			return 0, fmt.Errorf("invalid fps %q: %w", s, err)
		}
		return fps, nil
	case 2:
		num, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid fps numerator %q: %w", parts[0], err)
		}
		den, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid fps denominator %q: %w", parts[1], err)
		}
		if den == 0 {
			return 0, fmt.Errorf("invalid fps %q: denominator must not be zero", s)
		}
		return num / den, nil
	default:
		return 0, fmt.Errorf("invalid fps %q", s)
	}
}
