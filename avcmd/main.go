package main

import (
	"fmt"
	"os"

	"github.com/qluvio/avpipe/avcmd/cmd"
	"github.com/spf13/cobra"
)

func main() {
	cmdRoot := &cobra.Command{
		Use:          "avcmd",
		Short:        "Audio Video Command",
		Long:         "",
		SilenceUsage: false,
	}

	err := cmd.InitTranscode(cmdRoot)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}

	err = cmdRoot.Execute()
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
