package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"sync"
	"time"

	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"golang.org/x/net/ipv4"
)

func main() {
	url := flag.String("url", "", "UDP URL to read (e.g. udp://232.1.2.3:1234)")
	bufSize := flag.Int("buf", transport.PacketSize*7, "read buffer size in bytes")
	sleepPrecision := flag.Bool("sleep-precision", false, "measure 250us sleep precision instead of reading UDP")
	sleepPrecisionSeconds := flag.Float64("sleep-precision-seconds", 0, "sleep duration in seconds for -sleep-precision")
	flag.Parse()

	if *sleepPrecision || *sleepPrecisionSeconds > 0 {
		sleepDuration := 250 * time.Microsecond
		if *sleepPrecisionSeconds < 0 {
			log.Fatalf("sleep-precision-seconds must be >= 0")
		}
		if *sleepPrecisionSeconds > 0 {
			sleepDuration = time.Duration(*sleepPrecisionSeconds * float64(time.Second))
			if sleepDuration <= 0 {
				log.Fatalf("sleep-precision-seconds value too small")
			}
		}
		runSleepPrecisionTest(sleepDuration)
		return
	}

	if *url == "" {
		fmt.Println("Usage: live_udp_reader -url udp://232.1.2.3:1234")
		os.Exit(1)
	}

	log.Printf("Starting live UDP reader for %s", *url)

	udp := transport.NewUDPTransport(*url)
	rc, err := udp.Open()
	if err != nil {
		log.Fatalf("failed to open UDP transport: %v", err)
	}
	defer rc.Close()

	buf := make([]byte, *bufSize)
	oobBuf := make([]byte, 64)
	stats := make(map[string]*groupStats)
	var statsMu sync.Mutex

	udpConn, ok := rc.(*net.UDPConn)
	if !ok {
		log.Fatalf("bad UDP connection %v", rc)
	}

	ipConn := ipv4.NewPacketConn(udpConn)
	if err := ipConn.SetControlMessage(ipv4.FlagDst, true); err != nil {
		log.Fatalf("failed to enable dst control messages: %v", err)
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				statsMu.Lock()
				printStats(stats)
				statsMu.Unlock()
			case <-done:
				return
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	for {
		n, oobn, _, _, err := udpConn.ReadMsgUDP(buf, oobBuf)
		if n > 0 {
			dst := "unknown"
			if oobn > 0 {
				var cm ipv4.ControlMessage
				if parseErr := cm.Parse(oobBuf[:oobn]); parseErr == nil && cm.Dst != nil {
					dst = cm.Dst.String()
				}
			}
			statsMu.Lock()
			gs := ensureGroupStats(stats, dst)
			gs.Packets++
			gs.Bytes += uint64(n)
			statsMu.Unlock()
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Fatalf("read error: %v", err)
		}

		select {
		case <-sigCh:
			log.Println("Interrupt received, shutting down")
			close(done)
			return
		default:
		}
	}
	close(done)
}

func runSleepPrecisionTest(sleepDuration time.Duration) {
	type sleepBuckets struct {
		b0to1   uint64
		b1to5   uint64
		b5to10  uint64
		b10to20 uint64
		b20plus uint64
	}

	var (
		buckets   sleepBuckets
		bucketsMu sync.Mutex
	)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				bucketsMu.Lock()
				log.Printf("sleep oversleep buckets 0-1ms=%d 1-5ms=%d 5-10ms=%d 10-20ms=%d 20ms+=%d",
					buckets.b0to1, buckets.b1to5, buckets.b5to10, buckets.b10to20, buckets.b20plus)
				buckets = sleepBuckets{}
				bucketsMu.Unlock()
			case <-done:
				return
			}
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	log.Printf("Measuring sleep oversleep for %s sleeps", sleepDuration)

	for {
		start := time.Now()
		time.Sleep(sleepDuration)
		elapsed := time.Since(start)
		oversleep := elapsed - sleepDuration
		if oversleep < 0 {
			oversleep = 0
		}

		bucketsMu.Lock()
		switch {
		case oversleep < time.Millisecond:
			buckets.b0to1++
		case oversleep < 5*time.Millisecond:
			buckets.b1to5++
		case oversleep < 10*time.Millisecond:
			buckets.b5to10++
		case oversleep < 20*time.Millisecond:
			buckets.b10to20++
		default:
			buckets.b20plus++
		}
		bucketsMu.Unlock()

		select {
		case <-sigCh:
			log.Println("Interrupt received, shutting down")
			close(done)
			return
		default:
		}
	}
}

type groupStats struct {
	Packets uint64
	Bytes   uint64
}

func ensureGroupStats(m map[string]*groupStats, key string) *groupStats {
	if gs, ok := m[key]; ok {
		return gs
	}
	gs := &groupStats{}
	m[key] = gs
	return gs
}

func printStats(m map[string]*groupStats) {
	if len(m) == 0 {
		log.Println("No packets received yet")
		return
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		gs := m[k]
		log.Printf("[%s] packets=%d bytes=%d", k, gs.Packets, gs.Bytes)
	}
}
