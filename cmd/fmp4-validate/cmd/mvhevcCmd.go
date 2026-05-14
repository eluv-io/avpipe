package cmd

import (
	"github.com/eluv-io/avpipe/mp4e/mvhevc"
	"github.com/spf13/cobra"
)

var mvHevcCmd = &cobra.Command{
	Use:   "mvhevc <file>",
	Short: "validate and print MVHEVC metadata",
	Args:  cobra.ExactArgs(1),
	RunE:  runMvHevc,
}

func init() {
	mvHevcCmd.PersistentFlags().Bool("idr", false, "Show IDR (sync) frame positions")
}

func runMvHevc(cmd *cobra.Command, args []string) error {
	var opts mvhevc.InfoOptions
	var err error

	opts.ShowIDR, err = cmd.Flags().GetBool("idr")
	if err != nil {
		return err
	}
	opts.Json, err = cmd.Flags().GetBool("json")
	if err != nil {
		return err
	}

	path := args[0]
	return mvhevc.Info(path, opts, cmd.OutOrStdout())
}
