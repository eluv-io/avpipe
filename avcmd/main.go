package main

import (
	"fmt"
	"os"
	"path/filepath"

	elog "github.com/qluvio/content-fabric/log"

	"github.com/qluvio/avpipe"
	"github.com/qluvio/avpipe/avcmd/cmd"
	"github.com/spf13/cobra"
)

var log *elog.Log

func main() {
	cmdRoot := &cobra.Command{
		Use:          "avcmd",
		Short:        "Audio Video Command",
		Long:         "",
		SilenceUsage: false,
	}

	f := filepath.Join("", "avcmd.log")
	c := &elog.Config{
		Level:   "debug",
		Handler: "json",
		File: &elog.LumberjackConfig{
			Filename:   f,
			MaxSize:    10,
			MaxAge:     0,
			MaxBackups: 1,
			LocalTime:  false,
			Compress:   true,
		},
	}

	log = elog.New(c)
	log.Info("Starting avcmd")
	avpipe.SetCLoggers()

	err := cmd.InitTranscode(cmdRoot)
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
