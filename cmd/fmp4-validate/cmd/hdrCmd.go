package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/spf13/cobra"
	"os"
)

var hdrCmd = &cobra.Command{
	Use:   "hdr <file>",
	Short: "validate and print HDR10 HEVC metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runHdr,
}

func init() {
	hdrCmd.PersistentFlags().Bool("info", false, "print detailed HDR fields")
}

func runHdr(cmd *cobra.Command, args []string) error {
	path := args[0]
	jsonFlag, err := cmd.Flags().GetBool("json")
	if err != nil {
		return err
	}
	infoFlag, err := cmd.Flags().GetBool("info")
	if err != nil {
		return err
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	file, decodeErr := mp4.DecodeFile(f)
	if file == nil {
		return fmt.Errorf("parse error: %w", decodeErr)
	}

	hdrInfo, err := mp4e.ValidateHDR(file)
	if err != nil {
		return fmt.Errorf("could not validate HDR: %w", err)
	}

	if jsonFlag {
		report := hdrInfo.Report(infoFlag)
		if decodeErr != nil {
			report.ParseWarning = decodeErr.Error()
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	w := cmd.OutOrStdout()
	if decodeErr != nil {
		_, _ = fmt.Fprintf(w, "parse warning: %v\n", decodeErr)
	}

	out := hdrInfo.String()
	if infoFlag {
		out = out + "\n" + hdrInfo.InfoString()
	}
	fmt.Fprintln(w, out)
	return nil
}
