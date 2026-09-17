package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/eluv-io/avpipe/goavpipe/avdesc"
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
	return enc.Encode(render(infos))
}

// displayCodecInfo is avdesc.CodecInfo plus the human-readable names for its
// two numeric codec fields.
//
// The names are rendered here rather than by a MarshalJSON on the shared type:
// they are a display choice of this tool, they need mp4e's codec tables, and
// avdesc.CodecInfo is also a probe result, whose JSON should not carry fields
// the probe does not report.
type displayCodecInfo struct {
	*avdesc.CodecInfo
	ProfileName string `json:"profile_name,omitempty"`
	LevelName   string `json:"level_name,omitempty"`
}

func render(infos []*avdesc.CodecInfo) []displayCodecInfo {
	out := make([]displayCodecInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, displayCodecInfo{
			CodecInfo:   info,
			ProfileName: mp4e.ProfileName(info.CodecTagString, info.ProfileIDC),
			LevelName:   mp4e.LevelName(info.CodecTagString, info.Level),
		})
	}
	return out
}
