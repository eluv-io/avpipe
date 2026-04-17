package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/eluv-io/avpipe/goavpipe"
)

func main() {
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			fmt.Fprintln(os.Stderr, "invalid line, expected: <pts> <json>")
			continue
		}
		pts, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid pts: %v\n", err)
			continue
		}
		json := []byte(parts[1])
		if err := goavpipe.SetHdr10Plus(pts, json); err != nil {
			fmt.Fprintf(os.Stderr, "SetHdr10Plus failed: %v\n", err)
		} else {
			fmt.Printf("Set HDR10+ for PTS=%d\n", pts)
		}
	}
}
