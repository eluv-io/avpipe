package xc_test

import (
	"flag"
	"os"
	"testing"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/log-go"
)

func TestMain(m *testing.M) {
	flag.Parse()
	log.SetDefault(&log.Config{
		Level:   "debug",
		Handler: "text",
		File: &log.LumberjackConfig{
			Filename:  "../test_out/avpipe-test.log",
			LocalTime: true,
			MaxSize:   1000,
		},
	})
	avpipe.SetCLoggers()
	os.Exit(m.Run())
}
