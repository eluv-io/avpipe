package transport

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

func UdpReader(ts *Ts, outConn net.Conn) {

	var nPackets = 0

	conn, err := joinMulticast(ts.Cfg.Url)
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

		// PENDING(SS) must configure RTP processing based on input
		tsData, err := StripRTP(buf[:n])
		if err != nil {
			continue
		}

		// Process all TS packets in this payload
		for offset := 0; offset+188 <= len(tsData); offset += 188 {
			p, err := ToTSPacket(tsData[offset : offset+188])
			if err != nil {
				continue
			}
			ts.HandleTSPacket(p, outConn)
		}

		if ts.Cfg.MaxPackets > 0 && nPackets > int(ts.Cfg.MaxPackets) {
			fmt.Println("Max packets - exit")
			break
		}
	}
}
