package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
)

// udpDatagramPackets is the number of TS packets per UDP datagram (7*188=1316),
const udpDatagramPackets = 7

// openOutput returns the muxer's output sink
// - target "udp://" uses a UDP sender (note 'rtp://' not handled yet)
// - otherwise treated as a file path
func openOutput(dest string) (io.WriteCloser, error) {
	if strings.HasPrefix(dest, "rtp://") {
		return nil, fmt.Errorf("rtp:// output not supported yet: %q", dest)
	}
	if strings.HasPrefix(dest, "udp://") {
		return newUDPWriter(dest)
	}
	return os.Create(dest)
}

// udpWriter accepts whole 188-byte TS packets and sends them as full UDP datagrams
type udpWriter struct {
	conn    net.Conn
	buf     []byte
	npkt    int
	senderr uint64 // count of datagrams dropped due to send errors
}

func newUDPWriter(url string) (*udpWriter, error) {
	addr := strings.TrimPrefix(url, "udp://")
	conn, err := net.Dial("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("udp output dial %q: %w", addr, err)
	}
	log.Info("mpegts-xc udp output", "addr", addr)
	return &udpWriter{conn: conn, buf: make([]byte, 0, udpDatagramPackets*tsPacketSize)}, nil
}

func (w *udpWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	w.npkt++
	if w.npkt >= udpDatagramPackets {
		if err := w.flush(); err != nil {
			return 0, err
		}
	}
	return len(p), nil
}

func (w *udpWriter) flush() error {
	if len(w.buf) == 0 {
		return nil
	}
	// UDP send errors are non-fatal - counting for stats
	if _, err := w.conn.Write(w.buf); err != nil {
		w.senderr++
		if w.senderr == 1 || w.senderr%1000 == 0 {
			log.Warn("mpegts-xc udp output send error", "err", err, "dropped", w.senderr)
		}
	}
	w.buf = w.buf[:0]
	w.npkt = 0
	return nil
}

func (w *udpWriter) Close() error {
	w.flush()
	if w.senderr > 0 {
		log.Warn("mpegts-xc udp output: datagrams dropped on send", "dropped", w.senderr)
	}
	return w.conn.Close()
}
