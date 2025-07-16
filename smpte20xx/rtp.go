package main

// This handles UDP (unicast and multicast), RTP decapsulation, MPEGTS basic processing
import (
	"fmt"
	"log"
	"net"

	"github.com/Comcast/gots/v2/packet"
	"github.com/Comcast/gots/v2/psi"
)

func joinMulticast(multicastAddr string) (*net.UDPConn, error) {
	addr, err := net.ResolveUDPAddr("udp", multicastAddr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenMulticastUDP("udp", nil, addr)
	if err != nil {
		return nil, err
	}
	// Set buffer size if needed
	conn.SetReadBuffer(4 * 188 * 1024)
	return conn, nil
}

func stripRTP(data []byte) ([]byte, error) {
	if len(data) < 12+188 {
		return nil, fmt.Errorf("packet too short for RTP and TS")
	}
	return data[12:], nil
}

var continuityMap = make(map[int]uint8)

func toTSPacket(data []byte) (packet.Packet, error) {
	if len(data) < packet.PacketSize {
		return packet.Packet{}, fmt.Errorf("not enough data for TS packet")
	}
	var pkt packet.Packet
	copy(pkt[:], data[:packet.PacketSize])
	return pkt, nil
}

var myProgram = 1
var myPmt = -1
var myVideoPid = -1

func handleTSPacket(ts [packet.PacketSize]byte, outConn net.Conn) {
	pkt := packet.Packet(ts)
	pid := pkt.PID()

	// Continuity Counter check
	cc := uint8(pkt.ContinuityCounter())
	if lastCC, ok := continuityMap[pid]; ok {
		expected := (lastCC + 1) & 0x0F
		if cc != expected {
			//fmt.Printf("CC mismatch on PID 0x%x: expected %d, got %d\n", pid, expected, cc)
		}
	}
	continuityMap[pid] = cc

	if pid < 100 {
		// fmt.Println("GOT PACKET", pid, cc)
	}

	// Parse PAT to find my PMT PID
	if myPmt == -1 && pid == 0x0000 && pkt.PayloadUnitStartIndicator() {

		payload, err := pkt.Payload()
		if err != nil {
			fmt.Println("ERROR: failed to retrieve packet payload", "cc=", cc, err)
			return
		}

		pat, err := psi.NewPAT(payload)

		if err == nil {
			for program, pmtPID := range pat.ProgramMap() {
				fmt.Printf("PAT: Program %d -> PMT PID 0x%x\n", program, pmtPID)
				if program == myProgram {
					myPmt = pmtPID
				}
			}
		}
		return
	}

	// Parse PMT to find video PID
	if myVideoPid == -1 && pid == myPmt && pkt.PayloadUnitStartIndicator() {

		payload, err := pkt.Payload()
		if err != nil {
			fmt.Println("ERROR: failed to retrieve packet payload", "cc=", cc, err)
			return
		}

		pmt, err := psi.NewPMT(payload)
		if err == nil {
			for _, es := range pmt.ElementaryStreams() {
				fmt.Printf("PMT: stream type 0x%x on PID 0x%x\n", es.StreamType(), es.ElementaryPid())
				if es.StreamType() == StreamTypeJpegXS {
					myVideoPid = es.ElementaryPid()
					fmt.Println("VIDEO STREAM", myVideoPid)
				}
			}
		}
		return
	}

	// Process video PID
	if pid == myVideoPid {
		//fmt.Printf("Video PID 0x%x packet received\n", pid)
		processPacket(&pkt, outConn)
	}
}

func udpReader(outConn net.Conn) {

	var maxPackets = 1000000
	var nPackets = 0

	conn, err := joinMulticast("239.255.255.11:1234")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	buf := make([]byte, 2048)
	for {
		if nPackets > maxPackets {
			break
		}
		nPackets++
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Println("read error:", err)
			continue
		}
		tsData, err := stripRTP(buf[:n])
		if err != nil {
			continue
		}
		// Process all TS packets in this payload
		for offset := 0; offset+188 <= len(tsData); offset += 188 {
			p, err := toTSPacket(tsData[offset : offset+188])
			if err != nil {
				continue
			}
			handleTSPacket(p, outConn)
		}
	}
}
