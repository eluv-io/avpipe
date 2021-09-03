package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Comcast/gots/packet"
	"github.com/qluvio/avpipe/live"
	"github.com/qluvio/avpipe/ts"
	"github.com/qluvio/content-fabric/log"
	"io"
	"os"
)

func main() {
	address := flag.String("address", ":21001", "UDP address to listen on")
	fileName := flag.String("f", "", "Read from TS file")
	help := flag.Bool("h", false, "Print help")
	pid := flag.Int("pid", -1, "PID to filter by")
	flag.Parse()
	if *help {
		flag.Usage()
		return
	}

	log.SetDefault(&log.Config{
		Level:   "debug",
		Handler: "text",
		File: &log.LumberjackConfig{
			Filename:  "ts-test.log",
			LocalTime: true,
		},
	})

	udp := *fileName == ""
	var reader io.Reader
	var closer io.Closer
	if udp {
		_, rwc, err := live.NewTsReaderV2(*address)
		if err != nil {
			fmt.Printf("Error starting UDP reader on #{*address}: #{err}\n")
			return
		}
		reader = rwc
		closer = rwc
		// Not sure if packet.Sync is necessary
	} else {
		tsFile, err := os.Open(*fileName)
		if err != nil {
			fmt.Printf("Failed to open file #{*fileName}: #{err}\n")
			return
		}
		defer func() {
			err := tsFile.Close()
			if err != nil && !errors.Is(err, os.ErrClosed) {
				fmt.Printf("Error closing file #{file.Name()}: #{err}\n")
			}
		}()
		peekScanner := bufio.NewReader(tsFile)

		// Verify if sync-byte is present and seek to the first sync-byte
		_, err = packet.Sync(peekScanner)
		if err != nil {
			fmt.Println(err)
			return
		}

		reader = peekScanner
		closer = tsFile
	}

	tsInfo := make(chan ts.ScteSignal)
	tsPipe := ts.NewPipe(reader, closer, *pid, tsInfo)

	// Read UDP and throw away
	go func() {
		var pkt packet.Packet
		for {
			if _, err := io.ReadFull(tsPipe, pkt[:]); err != nil {
				if err != io.EOF && err != io.ErrUnexpectedEOF {
					fmt.Println(err)
				}
				break
			}
		}
		err := tsPipe.Close()
		if err != nil {
			fmt.Println(err)
		}
	}()

	// Read SCTE-35 cues from the channel
	fmt.Println("[")
	for signal := range tsInfo {
		if signal.Err != nil {
			fmt.Println(signal.Err)
		} else {
			jsonBytes, err := json.MarshalIndent(signal.SpliceInfo, "", "  ")
			if err != nil {
				fmt.Println(err)
			} else {
				fmt.Println(string(jsonBytes) + ",")
			}
		}
	}
	fmt.Println("]")
}
