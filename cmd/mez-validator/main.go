package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/eluv-io/avpipe/pkg/validate"
)

const (
	appName = "mez-validator"
)

var usg = `%s validates mez (fMP4) files with respect to
   - Uniform sample durations
   - Sequential MFHD sequence numbers (starting from 1)
   - Consistent TFDT base media decode times (starting from 0)
   - Average bitrate within specified limits

   It takes either an individual file or a directory as input.
   For a directory, it validates all files with the .mp4 extension

   The output is a concatenated concise JSON report for each file.
   In addition, the program return value is 1 if any file is invalid, 0 otherwise.

Usage of %s:
`

type options struct {
	minBitrateKbps int
	maxBitrateKbps int
}

func parseOptions(fs *flag.FlagSet, args []string) (*options, error) {
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, usg, appName, appName)
		fmt.Fprintf(os.Stderr, "\n%s [options] <mp4file_or_directory>\n\noptions:\n", appName)
		fs.PrintDefaults()
	}

	opts := options{}

	fs.IntVar(&opts.minBitrateKbps, "min-bitrate", 10, "minimum bitrate in kbps")
	fs.IntVar(&opts.maxBitrateKbps, "max-bitrate", 100000, "maximum bitrate in kbps")

	err := fs.Parse(args[1:])
	return &opts, err
}

func main() {
	if err := run(os.Args, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, w io.Writer) error {
	fs := flag.NewFlagSet(appName, flag.ContinueOnError)
	opts, err := parseOptions(fs, args)

	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}

	if len(fs.Args()) != 1 {
		fs.Usage()
		return fmt.Errorf("need input file or directory")
	}
	target := fs.Arg(0)

	// Check if target is a file or directory
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("error accessing %s: %w", target, err)
	}

	if info.IsDir() {
		// Validate all MP4 files in directory
		err = validateDirectory(target, opts.minBitrateKbps, opts.maxBitrateKbps)
	} else {
		// Validate single file
		err = validate.ValidateMezFile(target, opts.minBitrateKbps, opts.maxBitrateKbps)
	}

	if err != nil {
		return fmt.Errorf("Validation failed: %w", err)
	}
	return nil
}

// validateDirectory validates all MP4 files in a directory
func validateDirectory(dirPath string, minBitrateKbps, maxBitrateKbps int) error {
	files, err := os.ReadDir(dirPath)
	if err != nil {
		return fmt.Errorf("failed to read directory: %v", err)
	}

	mp4Files := []string{}
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(strings.ToLower(file.Name()), ".mp4") {
			mp4Files = append(mp4Files, filepath.Join(dirPath, file.Name()))
		}
	}

	if len(mp4Files) == 0 {
		return fmt.Errorf("no MP4 files found in directory: %s", dirPath)
	}

	allPass := true
	for _, file := range mp4Files {
		result, err := validate.ValidateMez(file, minBitrateKbps, maxBitrateKbps)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to validate file %s: %v", file, err)
			allPass = false
			continue
		}
		if !result.Valid {
			allPass = false
		}

		validate.PrintMezValidationResult(os.Stdout, file, result)
	}
	if !allPass {
		return fmt.Errorf("validation failed for one or more files")
	}

	return nil
}
