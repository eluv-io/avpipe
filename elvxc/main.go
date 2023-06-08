package main

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/eluv-io/log-go"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/elvxc/cmd"
	"github.com/spf13/cobra"
)

func main() {
	cmdRoot := &cobra.Command{
		Use:          "elvxc",
		Short:        "Audio Video Command",
		Long:         "",
		SilenceUsage: false,
	}

	log.SetDefault(&log.Config{
		Level:   "debug",
		Handler: "text",
		File: &log.LumberjackConfig{
			Filename:  "elvxc.log",
			LocalTime: true,
		},
	})
	avpipe.SetCLoggers()

	done := make(chan os.Signal, 1)

	// Notify this channel when a SIGINT is received
	signal.Notify(done, os.Interrupt)

	// Fire off a goroutine to loop until that channel receives a signal.
	// When a signal is received simply exit the program
	go func() {
		for _ = range done {
			os.Exit(0)
		}
	}()

	log.Info("Starting elvxc", "version", avpipe.Version())

	err := cmd.InitTranscode(cmdRoot)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = cmd.InitMux(cmdRoot)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = cmd.WarmupStress(cmdRoot)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = cmd.AnalyseLog(cmdRoot)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = cmd.AnalyseStream(cmdRoot)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = cmd.Probe(cmdRoot)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = cmdRoot.Execute()
	if err != nil {
		fmt.Printf("Command failed\n")
		os.Exit(1)
	}
}
