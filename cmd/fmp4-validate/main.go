package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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
	_, err = parseOptions(fs, args)

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

	_, info, err := mp4e.ValidateFmp4(fileReader)
	if err != nil {
		return fmt.Errorf("could not parse input file: %w", err)
	}

	_, err = w.Write([]byte(info.String()))
	return
}

type options struct {
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
	//fs.BoolVar(&opts.version, "version", false, "Get fmp4-validate version")

	err := fs.Parse(args[1:])
	return &opts, err
}
