// mpegts-xc reads an MPEGTS stream and transcodes the video PID while preserving all other PIDs unaltered.
// Primarily used to compress/downscale video.
// It recreates PCR on output and pads and paces the output to produce a broadcast-grade MPEGTS feed.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/broadcastproto/transport"
	"github.com/eluv-io/avpipe/mpegtsxc"
	elog "github.com/eluv-io/log-go"
)

var log = elog.Get("mpegts-xc")

func main() {
	url := flag.String("url", "", "input MPEGTS URL, e.g. udp://239.255.0.1:1234 (required)")
	encWidth := flag.Int("width", -1, "downscale target width (-1 = source width)")
	encHeight := flag.Int("height", -1, "downscale target height (-1 = source height)")
	ecodec := flag.String("ecodec", "libx264", "video encoder (e.g. libx264, libx265, h264_nvenc, hevc_nvenc)")
	dcodec := flag.String("dcodec", "", "video decoder (empty = auto; e.g. h264_cuvid, hevc_cuvid for NVDEC). For multi-GPU, scope with CUDA_VISIBLE_DEVICES.")
	videoBitrate := flag.Int("vb", -1, "video bitrate in bits/s (-1 = encoder default)")
	maxDatagrams := flag.Int64("max", 0, "stop after N UDP datagrams (0 = run until interrupted)")
	packaging := flag.String("packaging", "auto", "input packaging: auto (from url scheme), ts (raw MPEGTS), or rtp (RTP-wrapped)")
	outFile := flag.String("out", "out.ts", "output destination: a file path or udp://host:port")
	idleTimeout := flag.Duration("idle-timeout", 5*time.Second, "stop after no input is received for this long (0 = run until interrupted)")
	maxLead := flag.Duration("max-lead", time.Second, "max time passthrough packets may lead transcoded video")
	streamBitrate := flag.Int("stream-bitrate", 0, "exact output TS bitrate in bits/s for CBR output via null padding (0 = no pacing; set higher than -vb)")
	flag.Parse()

	elog.SetDefault(&elog.Config{Level: "debug", Handler: "text"})
	avpipe.SetCLoggers()

	if *url == "" {
		log.Error("mpegts-xc: -url is required")
		os.Exit(1)
	}

	log.Info("mpegts-xc starting", "url", *url, "width", *encWidth, "height", *encHeight, "ecodec", *ecodec)

	// Open the input transport - 'udp' or 'rtp' URL
	tr, err := openTransport(*url, *packaging)
	if err != nil {
		log.Error("mpegts-xc: failed to select transport", "err", err)
		os.Exit(1)
	}
	rc, err := tr.Open()
	if err != nil {
		log.Error("mpegts-xc: failed to open transport", "err", err)
		os.Exit(1)
	}
	defer rc.Close()

	out, err := openOutput(*outFile)
	if err != nil {
		log.Error("mpegts-xc: failed to open output", "out", *outFile, "err", err)
		os.Exit(1)
	}

	xc, err := mpegtsxc.NewLiveTranscoder(nil, mpegtsxc.Config{
		EncWidth:      int32(*encWidth),
		EncHeight:     int32(*encHeight),
		Ecodec:        *ecodec,
		Dcodec:        *dcodec,
		VideoBitrate:  int32(*videoBitrate),
		StreamBitrate: *streamBitrate,
		MaxLead:       *maxLead,
	}, out)
	if err != nil {
		log.Error("mpegts-xc: failed to start transcoder", "err", err)
		os.Exit(1)
	}

	// triggerStop closes the stop channel and the input reader (which blocks on UDP read)
	// Can be fired by a signal or idle watchdog (source idleness)
	stop := make(chan struct{})
	var stopOnce sync.Once
	triggerStop := func(reason string) {
		stopOnce.Do(func() {
			log.Info("mpegts-xc: stopping", "reason", reason)
			close(stop)
			rc.Close()
		})
	}

	// Graceful stop on SIGINT/SIGTERM.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		triggerStop("interrupt")
	}()

	// Idle input watchdog (for UDP/RTP input).
	// Applies only after the input connected (not a connection timeout)
	if *idleTimeout > 0 {
		go func() {
			ticker := time.NewTicker(time.Second)
			defer ticker.Stop()
			var last uint64
			started := false
			idleSince := time.Now()
			for {
				select {
				case <-stop:
					return
				case <-ticker.C:
					cur := xc.Stats().Datagrams
					switch {
					case cur != last:
						last = cur
						started = true
						idleSince = time.Now()
					case started && time.Since(idleSince) >= *idleTimeout:
						triggerStop(fmt.Sprintf("input idle for %s", *idleTimeout))
						return
					}
				}
			}
		}()
	}

	// Periodic stats
	statsTicker := time.NewTicker(5 * time.Second)
	defer statsTicker.Stop()
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-statsTicker.C:
				xc.LogStats()
				// In CBR mode the pacer should phase-lock to the source within a few seconds
				// Warn if that doesn't happen
				sn := xc.Stats()
				if sn.CbrMode && sn.Datagrams > 0 && !sn.PhaseLocked {
					log.Warn("mpegts-xc: phase-lock not acquired — pacer is on local clock so output will drift")
				}
			}
		}
	}()

	buf := make([]byte, 65536)

readLoop:
	for {
		select {
		case <-stop:
			break readLoop
		default:
		}

		n, rerr := rc.Read(buf)
		if rerr != nil {
			select {
			case <-stop:
			default:
				log.Error("mpegts-xc: UDP read error", "err", rerr)
			}
			break
		}
		if n == 0 {
			continue
		}

		_ = xc.Feed(buf[:n])

		if *maxDatagrams > 0 && xc.Stats().Datagrams >= uint64(*maxDatagrams) {
			log.Info("mpegts-xc: reached -max datagrams", "max", *maxDatagrams)
			break
		}
	}

	// Drain: close the avpipe xc feed and the FIFO and wait to complete
	err = xc.Finish()

	xc.LogStats() // final
	sn := xc.Stats()
	log.Info("mpegts-xc done",
		"droppedForward", sn.ForwardDropped, "fifoDropped", sn.FifoDropped,
		"videoPID", sn.VideoPID, "err", err)
}

// openTransport selects input packaging by either URL or input packaging configuration
func openTransport(url, packaging string) (transport.Transport, error) {
	if !strings.HasPrefix(url, "udp://") && !strings.HasPrefix(url, "rtp://") {
		return nil, fmt.Errorf("url must start with udp:// or rtp://: %q", url)
	}

	p := strings.ToLower(packaging)
	if p == "auto" {
		if strings.HasPrefix(url, "rtp://") {
			p = "rtp"
		} else {
			p = "ts"
		}
	}

	// The pipeline consumes raw 188-byte TS packets, so the transport must deliver RawTs
	// (for RTP input this strips the RTP header).
	switch p {
	case "ts":
		return transport.NewUDPTransport(url, transport.RawTs), nil
	case "rtp":
		return transport.NewRTPTransport(url, transport.RawTs), nil
	default:
		return nil, fmt.Errorf("unknown -packaging %q (want auto, ts, or rtp)", packaging)
	}
}
