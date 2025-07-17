package main

import (
	"flag"
	"fmt"
	"net"
	"os"

	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/pes"
)

type Config struct {
	url            *string
	saveFrameFiles *bool
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

	flag.Parse()
	cfg.Print()

	outConn, err := ConnectUnixSocket(outSock)
	if err != nil {
		fmt.Println("ERROR: failed to connect to output unix socket", err)
		os.Exit(-1)
	}

	udpReader(outConn)

	fmt.Print("UDP reader done")
}

var pesBuffer []byte
var collecting bool

func processPacket(pkt *packet.Packet, outConn net.Conn) error {

	payload, err := pkt.Payload()
	if err != nil {
		fmt.Println("ERROR: failed to retrieve packet payload", err)
		return err
	}

	if pkt.PayloadUnitStartIndicator() {
		if collecting && len(pesBuffer) > 0 {
			// Save the last PES packet
			//fmt.Println("save pes", len(pesBuffer))
			savePayload(pesBuffer, outConn)
		}
		pesBuffer = make([]byte, 0)
		collecting = true
	}

	if collecting {
		pesBuffer = append(pesBuffer, payload...)
	}

	return nil
}

func savePayload(pes []byte, outConn net.Conn) {
	if len(pes) < 9 {
		return
	}
	// Parse PES header length
	pesHeaderLength := int(pes[8])
	payloadStart := 9 + pesHeaderLength
	if payloadStart >= len(pes) {
		return
	}
	extractPayload(pes, outConn)
}

var nFrames = int(0)

func extractPayload(pesData []byte, outConn net.Conn) {

	outputFile := fmt.Sprintf("frame_%04d.jxs", nFrames)
	nFrames++

	ph, err := pes.NewPESHeader(pesData)
	if err != nil {
		fmt.Println("WARN: bad PES header", err)
	}

	s := ph.StreamId()
	a := ph.HasPTS()
	p := ph.PTS()
	t := ph.PacketStartCodePrefix()

	headerLength := int(pesData[8])
	if len(pesData) <= headerLength {
		panic("PES payload not found (file too small)")
	}

	// Locate the start of the JXS codestream
	payload := pesData[headerLength:]
	jxesMagic := []byte{0x6A, 0x78, 0x65, 0x73} // "jxes"
	idx := indexOf(payload, jxesMagic)
	if idx < 0 {
		fmt.Println("WARN: Could not find 'jxes' marker in PES payload")
		idx = 0 // write whole payload anyway
	}

	jxsData := payload[idx:]

	// Parse the jxes header
	jxsh, _ := ParseJXESHeader(jxsData)
	//jxsh.Print()
	_ = jxsh

	// Strip the jxes header
	jxsCodeStream, err := StripJXESHeader(jxsData)
	if err != nil {
		fmt.Println("WARN: Failed to extract JXS code stream", err)
	}
	_ = jxsCodeStream

	fmt.Println("PES ", nFrames, "stream", s, "haspts", a, "pts", p, "pfx", t, "len", len(payload), "len es", len(jxsData), "len cs", len(jxsCodeStream))

	// Save to file
	if *cfg.saveFrameFiles {
		if err := os.WriteFile(outputFile, jxsCodeStream, 0644); err != nil {
			panic(err)
		}
		fmt.Printf("Wrote %d bytes of JXS codestream to %s\n", len(jxsCodeStream), outputFile)
	}

	// Write to unix socket
	err = SendJXSFrame(outConn, jxsCodeStream)
	if err != nil {
		fmt.Println("WARN: failed to send JXS frame", err)
	}

}

// Basic search for a byte pattern
func indexOf(data, pattern []byte) int {
	for i := 0; i <= len(data)-len(pattern); i++ {
		match := true
		for j := 0; j < len(pattern); j++ {
			if data[i+j] != pattern[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
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

// SendJXSFrame sends a single JPEG XS frame to the connected Unix socket.
func SendJXSFrame(conn net.Conn, frame []byte) error {

	// We can only write frames of correct size since they are just concatenated
	// and any deviation breaks the parser
	const expectedSize = 458792
	if len(frame) != expectedSize {
		return fmt.Errorf("bad frame size: %d", len(frame))
	}

	n, err := conn.Write(frame)
	if err != nil {
		return fmt.Errorf("failed to write frame to socket: %w", err)
	}
	if n != len(frame) {
		return fmt.Errorf("partial write: wrote %d of %d bytes", n, len(frame))
	}
	return nil
}

func (c *Config) Print() {
	fmt.Println("Config:")
	if c.url != nil {
		fmt.Printf("  url: %s\n", *c.url)
	} else {
		fmt.Println("  url: <nil>")
	}

	if c.saveFrameFiles != nil {
		fmt.Printf("  saveFrameFiles: %t\n", *c.saveFrameFiles)
	} else {
		fmt.Println("  saveFrameFiles: <nil>")
	}
}
