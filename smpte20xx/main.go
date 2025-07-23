package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/eluv-io/avpipe/smpte20xx/transport"
)

type Config struct {
	url            *string
	saveFrameFiles *bool
	processVideo   *bool
	maxPackets     *int64
}

var cfg Config

const (
	PacketSize = 188
	SyncByte   = 0x47
	outSock    = "/tmp/elv_sock_jxs"
)

func main() {

	cfg.url = flag.String("url", "239.255.255.11:1234", "input url eg. udp://239.1.1.1")
	cfg.saveFrameFiles = flag.Bool("frames", false, "save frame files")
	cfg.processVideo = flag.Bool("video", false, "process video stream")
	cfg.maxPackets = flag.Int64("max", -1, "exit after processing these many packets")

	flag.Parse()
	cfg.Print()

	segCfg := transport.SegmenterConfig{
		DurationTs: 30 * 90000,
		Output: transport.Output{
			Kind:    transport.OutputFile,
			Locator: "OUT",
		},
	}
	tsCfg := transport.TsConfig{
		Url:            *cfg.url,
		SaveFrameFiles: *cfg.saveFrameFiles,
		ProcessVideo:   *cfg.processVideo,
		MaxPackets:     *cfg.maxPackets,
		SegCfg:         segCfg,
	}

	ts := transport.NewTs(tsCfg)

	var outConn net.Conn
	var err error
	if *cfg.processVideo {
		outConn, err = ConnectUnixSocket(outSock)
		if err != nil {
			fmt.Println("ERROR: failed to connect to output unix socket", err)
			os.Exit(-1)
		}
	}

	transport.UdpReader(ts, outConn)

	fmt.Println("UDP reader done")
}

// ConnectUnixSocket connects to a Unix domain socket at the given path.
func ConnectUnixSocket(socketPath string) (net.Conn, error) {
	addr := net.UnixAddr{
		Name: socketPath,
		Net:  "unix",
	}
	conn, err := net.DialUnix("unix", nil, &addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to unix socket: %w", err)
	}
	return conn, nil
}

func (c *Config) Print() {
	fmt.Println("Config:")
	fmt.Printf("  url: %s\n", *c.url)
	fmt.Printf("  saveFrameFiles: %t\n", *c.saveFrameFiles)
	fmt.Printf("  processVideo: %t\n", *c.processVideo)
}
