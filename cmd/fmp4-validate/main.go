package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/eluv-io/avpipe/mp4e"
	"github.com/eluv-io/avpipe/mp4e/dovi"
	"github.com/eluv-io/avpipe/mp4e/mvhevc"
	"github.com/eluv-io/avpipe/mp4e/sdr"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v2"
)

func main() {
	execute()
}

func execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

var rootCmd = &cobra.Command{
	Use:     "fmp4-validate",
	Short:   "parses and validates the input fmp4 (ISOBMFF) file",
	Args:    cobra.ExactArgs(1),
	RunE:    runFmp4Validate,
	Example: `fmp4-validate`,
}

func init() {
	rootCmd.PersistentFlags().Bool("json", false, "output json")
	rootCmd.PersistentFlags().Bool("info", false, "print detailed HDR fields")
	rootCmd.PersistentFlags().Bool("idr", false, "Show IDR (sync) frame positions")
	rootCmd.PersistentFlags().Bool("hdr", false, "validate and print HDR10 HEVC metadata")
	rootCmd.PersistentFlags().Bool("mvhevc", false, "print mvhevc metadata")
	rootCmd.PersistentFlags().Bool("atmos", false, "validate and print Dolby Atmos (EC-3+JOC or AC-4) metadata")
	rootCmd.PersistentFlags().Bool("dovi", false, "validate and print Dolby Vision metadata")
	rootCmd.PersistentFlags().Bool("sdr", false, "validate and print SDR metadata")
}

type Output struct {
	mp4e.HDRReport
	Atmos   *mp4e.AtmosReport `json:"atmos,omitempty"`
	MVHEVC  any               `json:"mvhevc,omitempty"`
	DV      any               `json:"dovi,omitempty"`
	SDR     any               `json:"sdr,omitempty"`
	Default any               `json:"default,omitempty"`
}

func runFmp4Validate(cmd *cobra.Command, args []string) error {
	path := args[0]

	jsonFlag, _ := cmd.Flags().GetBool("json")
	hdr, _ := cmd.Flags().GetBool("hdr")
	mvHevc, _ := cmd.Flags().GetBool("mvhevc")
	idr, _ := cmd.Flags().GetBool("idr")
	infoFlag, _ := cmd.Flags().GetBool("info")
	atmos, _ := cmd.Flags().GetBool("atmos")
	doviFlag, _ := cmd.Flags().GetBool("dovi")
	sdrFlag, _ := cmd.Flags().GetBool("sdr")

	var output Output
	var textOutput []string

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	ran := false

	// HDR
	if hdr {
		ran = true

		_, err = f.Seek(0, io.SeekStart)
		if err != nil {
			return err
		}

		file, decodeErr := mp4.DecodeFile(f)
		if file == nil {
			return fmt.Errorf("parse error: %w", decodeErr)
		}

		hdrInfo, err := mp4e.ValidateHDR(file)
		if err != nil {
			return err
		}

		if jsonFlag {
			report := hdrInfo.Report(infoFlag)
			if decodeErr != nil {
				report.ParseWarning = decodeErr.Error()
			}
			output.HDRReport = report
		} else {
			if decodeErr != nil {
				textOutput = append(textOutput, fmt.Sprintf("parse warning: %v\n", decodeErr))
			}
			textOutput = append(textOutput, hdrInfo.String())

			if infoFlag {
				textOutput = append(textOutput, hdrInfo.InfoString())
			}
		}
	}

	// Atmos
	if atmos {
		ran = true

		_, err = f.Seek(0, io.SeekStart)
		if err != nil {
			return err
		}

		file, decodeErr := mp4.DecodeFile(f)
		if file == nil {
			return fmt.Errorf("parse error: %w", decodeErr)
		}

		atmosInfo, err := mp4e.ValidateAtmos(file)
		if err != nil {
			return err
		}

		if jsonFlag {
			report := atmosInfo.Report(infoFlag)
			if decodeErr != nil && output.ParseWarning == "" {
				output.ParseWarning = decodeErr.Error()
			}
			output.Atmos = &report
		} else {
			if decodeErr != nil {
				textOutput = append(textOutput, fmt.Sprintf("parse warning: %v\n", decodeErr))
			}
			textOutput = append(textOutput, atmosInfo.String())

			if infoFlag {
				textOutput = append(textOutput, atmosInfo.InfoString())
			}
		}
	}

	// dolby vision
	if doviFlag {
		ran = true

		var opts dovi.InfoOptions
		opts.ShowIDR = idr

		info, err := dovi.Info(path, opts)
		if err != nil {
			return err
		}
		if jsonFlag {
			output.DV = info
		} else {
			b, err := yaml.Marshal(info)
			if err != nil {
				return err
			}
			textOutput = append(textOutput, fmt.Sprintf("%s\n", b))
		}
	}

	// SDR
	if sdrFlag {
		ran = true

		var opts sdr.InfoOptions
		opts.ShowIDR = idr

		info, err := sdr.Info(path, opts)
		if err != nil {
			return err
		}
		if jsonFlag {
			output.SDR = info
		} else {
			b, err := yaml.Marshal(info)
			if err != nil {
				return err
			}
			textOutput = append(textOutput, fmt.Sprintf("%s\n", b))
		}
	}

	// MVHEVC
	if mvHevc {
		ran = true

		var opts mvhevc.InfoOptions
		opts.ShowIDR = idr

		info, err := mvhevc.Info(path, opts)
		if err != nil {
			return err
		}

		if jsonFlag {
			output.MVHEVC = info
		} else {
			b, err := yaml.Marshal(info)
			if err != nil {
				return err
			}
			textOutput = append(textOutput, fmt.Sprintf("%s\n", b))
		}
	}

	// default
	if !ran {
		_, err = f.Seek(0, io.SeekStart)
		if err != nil {
			return err
		}

		_, info, err := mp4e.ValidateFmp4(f)
		if err != nil {
			return err
		}

		if jsonFlag {
			output.Default = info
		} else {
			textOutput = append(textOutput, info.String())
		}
	}

	// output
	if jsonFlag {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	_, err = fmt.Fprintln(cmd.OutOrStdout(),
		strings.Join(textOutput, "\n\n"))
	return err
}
