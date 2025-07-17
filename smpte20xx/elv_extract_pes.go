package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/Comcast/gots/v2/packet"
)

const (
	PacketSize = 188
	SyncByte   = 0x47
	TargetPID  = 0x0066 // Change as needed
	outSock    = "/tmp/elv_sock_jxs"
)

func main() {

	outConn, err := ConnectUnixSocket(outSock)
	if err != nil {
		fmt.Println("ERROR: failed to connect to output unix socket", err)
		os.Exit(-1)
	}

	udpReader(outConn)
}

func main2() {
	input, err := os.Open("input.ts")
	if err != nil {
		panic(err)
	}
	defer input.Close()

	reader := bufio.NewReader(input)

	var pesBuffer []byte
	var collecting bool

	for {
		packet := make([]byte, PacketSize)
		_, err := io.ReadFull(reader, packet)
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}

		if packet[0] != SyncByte {
			fmt.Println("Sync byte not found, skipping packet")
			continue
		}
		//fmt.Println("SYNC BYTE", packet[0])

		pid := int((int(packet[1]&0x1F)<<8 | int(packet[2])))
		payloadUnitStart := packet[1]&0x40 != 0
		adaptationFieldControl := (packet[3] >> 4) & 0x03
		payloadOffset := 4

		// Handle adaptation field
		if adaptationFieldControl == 2 || adaptationFieldControl == 0 {
			continue // No payload
		} else if adaptationFieldControl == 3 {
			adaptLen := int(packet[4])
			payloadOffset += 1 + adaptLen
		}

		if pid != TargetPID || payloadOffset >= PacketSize {
			continue
		}

		payload := packet[payloadOffset:]

		if payloadUnitStart {
			if collecting && len(pesBuffer) > 0 {
				// Save the last PES packet
				fmt.Println("sve pes", len(pesBuffer))
				savePayload(pesBuffer, nil)
			}
			pesBuffer = make([]byte, 0)
			collecting = true
		}

		if collecting {
			pesBuffer = append(pesBuffer, payload...)
		}
	}

	// Save final packet
	if collecting && len(pesBuffer) > 0 {
		fmt.Println("save pes - last")
		savePayload(pesBuffer, nil)
	}
	fmt.Println("Done writing output.jxs")
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
			fmt.Println("save pes", len(pesBuffer))
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

	// Save to file
	const saveFrameFiles = false
	if saveFrameFiles {
		if err := os.WriteFile(outputFile, jxsCodeStream, 0644); err != nil {
			panic(err)
		}
	}

	// Write to unix socket
	SendJXSFrame(outConn, jxsCodeStream)

	fmt.Printf("Wrote %d bytes of JXS codestream to %s\n", len(jxsData), outputFile)
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
	n, err := conn.Write(frame)
	if err != nil {
		return fmt.Errorf("failed to write frame to socket: %w", err)
	}
	if n != len(frame) {
		return fmt.Errorf("partial write: wrote %d of %d bytes", n, len(frame))
	}
	return nil
}
