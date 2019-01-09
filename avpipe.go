package avpipe

/*
 * An AVPipe takes one video source as an input and can produce one or more outputs.
 *
 */

import (
	"fmt"
	"io"
)

type AVType int

const (
	DASHManifest AVType = iota
	DASHVideoInit
	DASHVideoSegment
	DASHAudioInit
	DASHAUdioSegment
)

type AVParams map[string]string

type AVPipeOpener interface {
	Open(t AVType, segno int) (io.WriteCloser, error)
}

type AVPipe struct {
	reader io.ReadCloser
	opener AVPipeOpener
	params *AVParams

	wseg io.WriteCloser /* Segment writer */
}

func NewAVPipe(r io.ReadCloser, o AVPipeOpener, params *AVParams) (*AVPipe, error) {
	return &AVPipe{reader: r, opener: o, params: params}, nil
}

/*
 * Run one encoding cycle - this should be run in a loop until it returns 'end of stream'
 */
func (avp *AVPipe) RunOnce() error {
	return nil
}

/*
 * Run the full encoding process block until it finishes.
 *
 * Mock implementation
 */
func (avp *AVPipe) Run() error {

	/* Test parameter - test_segment_size */
	testSegmentSize := 7000

	var err error

	buf := make([]byte, 1024)

	/* Mock implementation - read input and pass back to output */
	for {

		twr := 0 /* total bytes written */
		for {
			rd, rerr := avp.reader.Read(buf)
			if rd > 0 {

				if avp.wseg == nil {
					avp.wseg, err = avp.opener.Open(DASHVideoSegment, 0)
					if err != nil {
						return err
					}
				}

				wr, werr := avp.wseg.Write(buf[:rd])
				twr = twr + wr
				if werr != nil {
					return werr
				}
				if wr != rd {
					return fmt.Errorf("internal error %d != %d", wr, rd)
				}
			}
			if twr >= testSegmentSize {
				avp.wseg.Close()
				avp.wseg = nil
				break
			}
			if rerr == io.EOF {
				/* All done */
				if avp.wseg != nil {
					avp.wseg.Close()
					avp.wseg = nil
				}
				return nil
			}
			if rerr != nil {
				return rerr
			}
		}
	}

	return nil
}

func (avp *AVPipe) Close() error {
	avp.reader.Close()
	return nil
}
