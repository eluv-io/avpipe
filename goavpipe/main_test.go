package goavpipe

import (
	"os"
	"testing"

	log "github.com/eluv-io/log-go"
)

func TestMain(m *testing.M) {
	log.SetDefault(&log.Config{
		Level:   "debug",
		Handler: "text",
		File: &log.LumberjackConfig{
			Filename:  "../test_out/avpipe-test.log",
			LocalTime: true,
			MaxSize:   1000,
		},
	})
	os.Exit(m.Run())
}
