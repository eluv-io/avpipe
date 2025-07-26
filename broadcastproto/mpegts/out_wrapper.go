package mpegts

import (
	"io"
)

type SequentialWriter interface {
	OpenNext() (io.WriteCloser, error)
}

type mpegtsSequentialOutOpener struct {
	inFd int64
}

func NewMPEGTSSequentialOutWriter(inFd int64) SequentialWriter {
	return &mpegtsSequentialOutOpener{
		inFd: inFd,
	}
}
