package ts

import (
	"errors"
	"github.com/Comcast/gots/packet"
	"github.com/Comcast/gots/psi"
	"github.com/Comcast/gots/scte35"
	elog "github.com/qluvio/content-fabric/log"
	"io"
	"os"
)

var log = elog.Get("/eluvio/avpipe/ts")

// TODO The Tee is unnecessary once we have code to parse TS packets directly
// Implements io.ReadCloser
type Pipe struct {
	channel   chan<- ScteSignal
	in        io.Reader // source stream
	inCloser  io.Closer
	out       io.Reader // output stream (passthrough, same as source)
	sctePID   int
	tsReader  io.ReadCloser  // to TS parser
	teeWriter io.WriteCloser // keep this reference to close
}

type ScteSignal struct {
	SpliceInfo SpliceInfo
	Err        error
}

func NewPipe(source io.Reader, sourceCloser io.Closer, sctePID int,
	tsInfo chan<- ScteSignal) *Pipe {

	pipeReader, pipeWriter := io.Pipe()
	p := &Pipe{
		channel:   tsInfo,
		in:        source,
		inCloser:  sourceCloser,
		out:       io.TeeReader(source, pipeWriter),
		sctePID:   sctePID,
		tsReader:  pipeReader,
		teeWriter: pipeWriter,
	}
	go func() {
		p.readTS()
	}()
	return p
}

func (t *Pipe) Close() (err error) {
	e := t.tsReader.Close()
	if e != nil && !errors.Is(err, os.ErrClosed) {
		err = e
		log.Error("Pipe.Close tsReader", err)
	}
	e = t.teeWriter.Close()
	if e != nil && !errors.Is(err, os.ErrClosed) {
		err = e
		log.Error("Pipe.Close teeWriter", err)
	}
	e = t.inCloser.Close()
	if e != nil && !errors.Is(err, os.ErrClosed) {
		err = e
		log.Error("Pipe.Close in", err)
	}
	if t.channel != nil {
		close(t.channel)
		t.channel = nil
	}
	return
}

func (t *Pipe) Read(p []byte) (n int, err error) {
	return t.out.Read(p)
}

func (t *Pipe) readTS() {
	defer func() {
		_ = t.Close()
	}()

	pat, err := psi.ReadPAT(t.tsReader)
	if err != nil {
		log.Error("Failed to read PAT", err)
	} else {
		log.Debug("PAT", "PMT PIDs", pat.ProgramMap(),
			"Number of Programs", pat.NumPrograms())
	}

	var pmts []psi.PMT
	if pat != nil {
		pm := pat.ProgramMap()
		for pn, pid := range pm {
			pmt, err := psi.ReadPMT(t.tsReader, pid)
			if err != nil {
				log.Error("Failed to read PMT", err)
			} else {
				pmts = append(pmts, pmt)
				printPMT(pn, pmt)
			}
		}
	}

	var pkt packet.Packet
	var numPackets uint64
	var sctePackets uint64
	scte35PIDs := make(map[uint16]bool)
	for _, pmt := range pmts {
		for _, es := range pmt.ElementaryStreams() {
			if es.StreamType() == psi.PmtStreamTypeScte35 {
				scte35PIDs[es.ElementaryPid()] = true
				break
			}
		}
	}

	for {
		if _, err = io.ReadFull(t.tsReader, pkt[:]); err != nil {
			if err != io.EOF && err != io.ErrUnexpectedEOF && err != io.ErrClosedPipe {
				log.Error("Read error", err)
				sendError(t.channel, err)
			}
			break
		}
		numPackets++
		if numPackets%1000000 == 0 {
			log.Debug("Total TS packets read", "count", numPackets)
		}
		currPID := packet.Pid(&pkt)
		if !scte35PIDs[currPID] || (t.sctePID >= 0 && int(currPID) != t.sctePID) {
			continue
		}
		sctePackets++
		pay, err := packet.Payload(&pkt)
		if err != nil {
			log.Error("Error getting packet payload", err, "packet number", numPackets, "PID", currPID)
			sendError(t.channel, err)
			continue
		}
		msg, err := scte35.NewSCTE35(pay)
		if err != nil {
			log.Error("Error parsing SCTE-35", err)
			sendError(t.channel, err)
			continue
		}
		if msg.CommandInfo().CommandType() == scte35.SpliceNull {
			continue
		}
		si, err := ConvertGots(currPID, msg)
		if err != nil {
			log.Error("Error converting SCTE data", err)
			sendError(t.channel, err)
			continue
		}
		s := ScteSignal{
			SpliceInfo: si,
		}
		t.channel <- s
	}

	log.Debug("SCTE35 packets read", "count", sctePackets)
	log.Debug("Total packets read", "count", numPackets)
}

func printPMT(pn uint16, pmt psi.PMT) {
	log.Debug("PMT", "program", pn, "PIDs", pmt.Pids())
	for _, es := range pmt.ElementaryStreams() {
		log.Debug("Elementary Stream", "PID", es.ElementaryPid(),
			"StreamType", es.StreamType(), "description", es.StreamTypeDescription())
		for _, d := range es.Descriptors() {
			log.Debug("Descriptor", "tag", d)
		}
	}
}

func sendError(c chan<- ScteSignal, err error) {
	s := ScteSignal{
		Err: err,
	}
	c <- s
}
