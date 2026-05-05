package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/mp4e"
)

const appName = "fmp4-validate"

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) (err error) {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	opts, err := parseOptions(fs, args)

	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return
	}

	if len(fs.Args()) != 1 {
		fs.Usage()
		return fmt.Errorf("need input file")
	}
	inFilePath := fs.Arg(0)

	fileReader, err := os.Open(inFilePath)
	if err != nil {
		return fmt.Errorf("could not open input file: %w", err)
	}
	defer func(f *os.File) {
		_ = f.Close()
	}(fileReader)

	if opts.json && !opts.hdr && !opts.info {
		opts.info = true
	}
	if opts.info {
		opts.hdr = true
	}
	if opts.json {
		opts.hdr = true
	}

	if opts.hdr {
		file, decodeErr := mp4.DecodeFile(fileReader)
		if file == nil {
			return fmt.Errorf("could not parse input file: %w", decodeErr)
		}
		hdrInfo, err := mp4e.ValidateHDR(file)
		if err != nil {
			return fmt.Errorf("could not validate HDR: %w", err)
		}
		if opts.json {
			report := hdrInfo.Report(opts.info)
			if decodeErr != nil {
				report.ParseWarning = decodeErr.Error()
			}
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(report)
		}
		if decodeErr != nil {
			_, _ = fmt.Fprintf(w, "parse warning: %v\n", decodeErr)
		}
		if opts.info {
			_, err = fmt.Fprintf(w, "%s\n%s\n", hdrInfo.String(), hdrInfo.InfoString())
		} else {
			_, err = fmt.Fprintf(w, "%s\n", hdrInfo.String())
		}
		return err
	}

	_, info, err := mp4e.ValidateFmp4(fileReader)
	if err != nil {
		return fmt.Errorf("could not parse input file: %w", err)
	}

	_, err = w.Write([]byte(info.String()))
	return
}

type options struct {
	hdr  bool
	info bool
	json bool
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	const usg = `%s parses and validates the input fmp4 (ISOBMFF) file.

Usage of %s:
`

	fs.Usage = func() {
		_, _ = fmt.Fprintf(os.Stderr, usg, appName, appName)
		_, _ = fmt.Fprintf(os.Stderr, "\n%s [options] infile\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}
	fs.BoolVar(&opts.hdr, "hdr", false, "validate and print HDR10 HEVC metadata")
	fs.BoolVar(&opts.info, "info", false, "print detailed HDR fields; implies -hdr")
	fs.BoolVar(&opts.json, "json", false, "print HDR output as JSON; implies -info")
	//fs.BoolVar(&opts.version, "version", false, "Get fmp4-validate version")

	err := fs.Parse(args[1:])
	return &opts, err
}
