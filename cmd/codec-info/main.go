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
	fs.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, "%s extracts codec information from an MP4 or fMP4 init segment and prints it as JSON.\n\nUsage: %s <file>\n", appName, appName)
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

	infos, err := mp4e.ExtractCodecInfo(f)
	if err != nil {
		return fmt.Errorf("could not extract codec info: %w", err)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(infos)
}
