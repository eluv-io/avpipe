package testutil

import (
	"os/exec"

	"github.com/eluv-io/log-go"
)

// NvidiaExist reports whether an NVIDIA GPU is available by running nvidia-smi
// and checking its exit code.
func NvidiaExist() bool {
	c := exec.Command("nvidia-smi")
	c.Stdout = nil
	c.Stderr = nil
	if err := c.Run(); err == nil {
		return true
	}
	log.Info("NVIDIA doesn't exist")
	return false
}
