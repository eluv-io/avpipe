package transport

import (
	"bufio"
	"fmt"
	"io"
	"os"

	"github.com/Comcast/gots/v2/packet"
	"github.com/eluv-io/avpipe/goavpipe"
)

type SequentialOpener interface {
	OpenNext(fd int64) (io.WriteCloser, error)
}

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

	seqOpener        SequentialOpener
	inFd             int64 // Corresponding input "fd"
	currentSeqWriter io.WriteCloser

	numSegs       int64
	startPcr      uint64
	segStartPcr   uint64
	currentPcr    uint64
	currentFile   *os.File
	currentWriter *bufio.Writer
}

func NewSegmenter(segCfg SegmenterConfig, seqOpener SequentialOpener, inFd int64) *Segmenter {

	s := Segmenter{
		Cfg:       segCfg,
		seqOpener: seqOpener,
		inFd:      inFd,
	}

	if s.Cfg.Output.Kind == OutputFile {
		if s.Cfg.Output.Locator == "" {
			s.Cfg.Output.Locator = "."
		}
		err := os.MkdirAll(s.Cfg.Output.Locator, 0755)
		if err != nil {
			fmt.Println("ERROR: failed to create output directory")
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
		fmt.Println("SEG END", "pcr", pcr, "start pcr", s.segStartPcr, "diff", pcr-s.segStartPcr)
		err = s.openSegment()
		if err != nil {
			return
		}
		s.segStartPcr = pcr
	}

	// Write test file
	_, err = s.currentWriter.Write(pkt[:])
	if err != nil {
		fmt.Println("ERROR: failed to write test output", err)
	}

	// Write output callback
	bytesWritten, err = s.currentSeqWriter.Write(pkt[:])

	return
}

func (s *Segmenter) openSegment() error {
	var err error
	s.closeSegment()
	s.numSegs++

	// Open test file writer
	fileName := fmt.Sprintf("%s/outseg_%04d.ts", s.Cfg.Output.Locator, s.numSegs)
	s.currentFile, err = os.Create(fileName)
	if err != nil {
		fmt.Println("ERROR: failed to open segment file", err)
		return err
	}
	s.currentWriter = bufio.NewWriter(s.currentFile)

	// Open seq writer
	if s.seqOpener != nil {
		s.currentSeqWriter, err = s.seqOpener.OpenNext(s.inFd)
		if err != nil {
			goavpipe.Log.Error("ERROR: failed to open next MPEGTS writer", err)
		}
	}
	return err
}

func (s *Segmenter) closeSegment() {
	if s.currentFile != nil {
		s.currentWriter.Flush()
		s.currentFile.Close()
		s.currentFile = nil
	}

	if s.currentSeqWriter != nil {
		s.currentSeqWriter.Close()
		s.currentSeqWriter = nil
	}
}
