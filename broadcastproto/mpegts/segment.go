package mpegts

import (
	"bufio"
	"fmt"
	"os"

	elog "github.com/eluv-io/log-go"

	"github.com/Comcast/gots/v2/packet"
)

// TODO(Nate): Track goroutine ID and use it with the existing log wrapper in goavpipe/log.go
var log = elog.Get("avpipe/broadcastproto/mpegts")

const PcrTs uint64 = 27_000_000

type OutputKind int

const (
	OutputNone OutputKind = iota
	OutputFile
	OutputUdp
)

type Output struct {
	Kind    OutputKind
	Locator string
}

type SegmenterConfig struct {
	DurationSec uint64
	Output      Output
}

type Segmenter struct {
	Cfg SegmenterConfig

	numSegs       int64
	startPcr      uint64
	segStartPcr   uint64
	currentPcr    uint64
	currentFile   *os.File
	currentWriter *bufio.Writer
}

func NewSegmenter(segCfg SegmenterConfig) *Segmenter {

	s := Segmenter{}
	s.Cfg = segCfg

	if s.Cfg.Output.Kind == OutputFile {
		if s.Cfg.Output.Locator == "" {
			s.Cfg.Output.Locator = "."
		}
		err := os.MkdirAll(s.Cfg.Output.Locator, 0755)
		if err != nil {
			log.Error("failed to create output directory", "err", err)
			return nil
		}
	}

	s.openSegment()
	return &s
}

func (s *Segmenter) WritePacket(pkt packet.Packet, pcr uint64) (bytesWritten int, err error) {
	if s.currentFile == nil {
		err = fmt.Errorf("ERROR: no segment to write to")
		return
	}
	if s.startPcr == 0 {
		s.startPcr = pcr
	}
	if s.segStartPcr == 0 {
		s.segStartPcr = pcr
	}
	s.currentPcr = pcr

	if pcr > s.segStartPcr && pcr-s.segStartPcr > s.Cfg.DurationSec*PcrTs {
		log.Debug("SEG END", "pcr", pcr, "start pcr", s.segStartPcr, "diff", pcr-s.segStartPcr)
		err = s.openSegment()
		if err != nil {
			return
		}
		s.segStartPcr = pcr
	}
	bytesWritten, err = s.currentWriter.Write(pkt[:])
	return
}

func (s *Segmenter) openSegment() error {
	var err error
	s.closeSegment()
	s.numSegs++
	fileName := fmt.Sprintf("%s/outseg_%04d.ts", s.Cfg.Output.Locator, s.numSegs)
	s.currentFile, err = os.Create(fileName)
	if err != nil {
		log.Error("failed to open segment file", "err", err)
		return err
	}
	s.currentWriter = bufio.NewWriter(s.currentFile)
	return err
}

func (s *Segmenter) closeSegment() {
	if s.currentFile != nil {
		s.currentWriter.Flush()
		s.currentFile.Close()
		s.currentFile = nil
	}
}
