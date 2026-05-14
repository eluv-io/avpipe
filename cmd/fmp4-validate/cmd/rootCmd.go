package cmd

import (
	"github.com/spf13/cobra"
	"os"
)

var rootCmd = &cobra.Command{
	Use:   "fmp4-validate",
	Short: "fMP4 inspection tool",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().Bool("json", false, "output json")
	rootCmd.AddCommand(hdrCmd)
	rootCmd.AddCommand(fmp4Cmd)
	rootCmd.AddCommand(mvHevcCmd)
}
