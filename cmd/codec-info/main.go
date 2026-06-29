package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/eluv-io/avpipe/mp4e"
)

const appName = "codec-info"

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) error {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	var initFile string
	fs.StringVar(&initFile, "init", "", "init segment to pair with a bare media (m4s) segment, without concatenating; the media's slices are parsed against this init's SPS/PPS")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, "%s extracts codec information from an MP4/fMP4 segment and prints it as JSON.\n\nUsage:\n  %s <file>\n  %s -init <init.m4s> <media.m4s>\n", appName, appName, appName)
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

	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("could not open input file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var infos []*mp4e.CodecInfo
	if initFile != "" {
		initF, ierr := os.Open(initFile)
		if ierr != nil {
			return fmt.Errorf("could not open init file: %w", ierr)
		}
		defer func() { _ = initF.Close() }()
		infos, err = mp4e.ExtractCodecInfoWithInit(f, initF)
	} else {
		infos, err = mp4e.ExtractCodecInfo(f)
	}
	if err != nil {
		return fmt.Errorf("could not extract codec info: %w", err)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(infos)
}
