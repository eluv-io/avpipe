package main

// This handles UDP (unicast and multicast), RTP decapsulation, MPEGTS basic processing
import (
	"fmt"
	"log"
	"net"
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
	conn.SetReadBuffer(10 * 1024 * 1024)
	return conn, nil
}

func stripRTP(data []byte) ([]byte, error) {
	if len(data) < 12+188 {
		return nil, fmt.Errorf("packet too short for RTP and TS")
	}
	return data[12:], nil
}

func udpReader(outConn net.Conn) {

	var nPackets = 0

	conn, err := joinMulticast(*cfg.url)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	buf := make([]byte, 2048)
	for {
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
