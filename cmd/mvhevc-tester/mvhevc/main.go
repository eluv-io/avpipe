package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/eluv-io/avpipe/mp4e/mvhevc"
)

const appName = "mvhevc"

var usage = `%s handles MV-HEVC helper operations.

Subcommands:
  info [-idr] <input.mp4>
  add [-fps <rate>] [-spatial] <input.hevc|mp4> <output.mp4>

Usage of %s:
`

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) error {
	if len(args) < 2 {
		printUsage()
		return fmt.Errorf("need subcommand: info or add")
	}

	switch args[1] {
	case "info":
		return runInfo(args[1:], w)
	case "add":
		return runAdd(args[1:], w)
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

func runAdd(args []string, w io.Writer) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	var opts mvhevc.AddOptions
	var baseline uint
	var hfov uint
	fs.Float64Var(&opts.FPS, "fps", 0, "Frame rate (required for .hevc input, e.g., 23.976, 24, 30, 60)")
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
	return mvhevc.Add(fs.Arg(0), fs.Arg(1), opts, w)
}

func printUsage() {
	_, _ = fmt.Fprintf(os.Stderr, usage, appName, appName)
}
