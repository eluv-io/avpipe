package live

import (
	"io"
	"net"
	"strings"
	"time"
)

type TsReader struct {
	addr       string // For example ":21001" (for localhost port 21001)
	pktLimit   int
	w          io.Writer
	done       chan bool
	errChannel chan error
}

// Deprecated
func NewTsReader(addr string, w io.Writer) *TsReader {

	tsr := &TsReader{
		addr:       addr,
		w:          w,
		errChannel: make(chan error, 10),
	}

	var err error
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") {
		err = tsr.serveFromFile(w)
	} else {
		err = tsr.serveOneConnection(w)
	}
	if err != nil {
		log.Error("TsReader failed", "err", err)
	}

	return tsr
}

// NewTsReaderV2  creates a UDP MPEG-TS reader and returns an io.Reader
// Starts the necessary goroutines - when the returned reader is closed, it stops
// all goroutines and cleans up.
func NewTsReaderV2(addr string) (io.ReadWriteCloser, error) {

	rwb := NewRWBuffer(100000)

	tsr := &TsReader{
		addr:       addr,
		w:          rwb,
		errChannel: make(chan error, 10),
	}

	var err error
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") {
		err = tsr.serveFromFile(rwb)
	} else {
		err = tsr.serveOneConnection(rwb)
	}
	if err != nil {
		log.Error("TsReader failed", "err", err)
	}

	return rwb, err
}

func readUdp(conn net.PacketConn, w io.Writer) error {

	// Assume that Close() is implemented, and that writer is not used after
	// this call
	defer func() {
		w.(io.WriteCloser).Close()
		err := conn.Close()
		log.Info("Closing UDP socket", "err", err, "addr", conn.LocalAddr().String())
	}()

	// Stop recording if nothing was read for timeout
	timeout := 5 * time.Second

	bytesRead := 0
	buf := make([]byte, 65536)

	for {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return err
		}

		n, sender, err := conn.ReadFrom(buf)
		bytesRead += n
		if err != nil {
			if err.(net.Error).Timeout() {
				if bytesRead == 0 {
					continue // waiting for stream start
				}
				log.Info("Stopped receiving UDP packets",
					"timeout", timeout, "bytesRead", bytesRead)
				return err
			}
			log.Error("UDP read failed", "err", err, "sender", sender)
			return err
		}

		t := time.Now()
		bw, err := w.Write(buf[:n])
		if err != nil || bw != n {
			log.Error("Failed to write UDP packet", "err", err, "bw", bw, "n", n, "sender", sender)
			return err
		}
		if time.Since(t) > time.Millisecond*10 || bw > 1500 {
			log.Warn("Writing UDP to avpipe took longer than expected", "timeSpent", time.Since(t), "written", bw)
		}
	}
	return nil
}

func (tsr *TsReader) serveOneConnection(w io.Writer) (err error) {

	sAddr, err := net.ResolveUDPAddr("udp", tsr.addr)
	if err != nil {
		return
	}
	conn, err := net.ListenUDP("udp", sAddr)
	if err != nil {
		return
	}
	// TODO: Make if a config param (RM)
	err = conn.SetReadBuffer(16 * 1024 * 1024)
	if err != nil {
		log.Error("Failed to set UDP buffer size, continue ...", err)
	}

	log.Info("ts_recorder server accepted", "addr", tsr.addr)

	go func(tsr *TsReader) {
		if err := readUdp(conn, w); err != nil {
			log.Error("Failed reading UDP stream", "err", err)
			tsr.errChannel <- err
		}
	}(tsr)

	return
}

func (tsr *TsReader) serveFromFile(w io.Writer) (err error) {

	/* Not implemented */
	return
}
