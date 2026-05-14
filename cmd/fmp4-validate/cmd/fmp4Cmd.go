package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/spf13/cobra"
	"os"
)

var fmp4Cmd = &cobra.Command{
	Use:   "fmp4 <file>",
	Short: "parses and validates the input fmp4 (ISOBMFF) file",
	Args:  cobra.ExactArgs(1),
	RunE:  runFmp4,
}

func runFmp4(cmd *cobra.Command, args []string) error {
	path := args[0]
	jsonFlag, err := cmd.Flags().GetBool("json")
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

	_, info, err := mp4e.ValidateFmp4(f)
	if err != nil {
		return fmt.Errorf("could not parse input file: %w", err)
	}

	if jsonFlag {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}
	_, err = cmd.OutOrStdout().Write([]byte(info.String()))
	return err
}
