package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	fifo := flag.String("fifo", "/tmp/hdr10p.fifo", "fifo path (must exist)")
	count := flag.Int("count", 10, "number of messages to send")
	start := flag.Int64("start", 1000, "starting PTS value")
	step := flag.Int64("step", 3000, "PTS increment per frame")
	delay := flag.Int("delay_ms", 100, "delay between writes in milliseconds")
	flag.Parse()

	f, err := os.OpenFile(*fifo, os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "unable to open fifo %s: %v\n", *fifo, err)
		os.Exit(1)
	}
	defer func() {
		if err := f.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "error closing fifo: %v\n", err)
		}
	}()

	for i := 0; i < *count; i++ {
		pts := *start + int64(i)*(*step)
		json := fmt.Sprintf("{\"PayloadVersion\":1,\"FrameNum\":%d}", i)
		line := fmt.Sprintf("%d %s\n", pts, json) // <PTS><space><JSON> expected by exc
		if _, err := f.WriteString(line); err != nil {
			fmt.Fprintf(os.Stderr, "write failed: %v\n", err)
			os.Exit(1)
		}
		time.Sleep(time.Duration(*delay) * time.Millisecond)
	}
}
