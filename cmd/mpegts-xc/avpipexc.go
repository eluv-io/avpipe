package main

import (
	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
)

// videoInputOpener / videoInput are the avpipe custom AVIO reader.
// avpipe xc reads just video and PAT/PMT packets from the source MPEGTS straeam
// (pre-filtered by the main MPEGTS reader)
type videoInputOpener struct {
	ch <-chan []byte
}

func (o *videoInputOpener) Open(fd int64, url string) (goavpipe.InputHandler, error) {
	log.Debug("mpegts-xc input Open", "fd", fd, "url", url)
	return &videoInput{ch: o.ch}, nil
}

type videoInput struct {
	ch   <-chan []byte
	left []byte // bytes from the last chunk not yet handed to avpipe
}

func (i *videoInput) Read(buf []byte) (int, error) {
	if len(i.left) == 0 {
		chunk, ok := <-i.ch
		if !ok {
			return 0, nil // closed channel => EOF (avpipe contract: (0, nil))
		}
		i.left = chunk
	}
	n := copy(buf, i.left)
	i.left = i.left[n:]
	return n, nil
}

func (i *videoInput) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (i *videoInput) Close() error                                 { return nil }
func (i *videoInput) Size() int64                                  { return -1 } // live, unknown
func (i *videoInput) Stat(streamIndex int, statType goavpipe.AVStatType, statArgs interface{}) error {
	return nil
}

// videoOutputOpener / videoOutput are the avpipe custom AVIO writer
type videoOutputOpener struct {
	videoOutCh chan<- videoPkt
	classifier *Classifier
	pcrLead    int64
	cbr        bool
}

func (o *videoOutputOpener) Open(h, fd int64, streamIndex, segIndex int,
	pts int64, outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	log.Info("mpegts-xc avpipe output open",
		"h", h, "fd", fd, "stream", streamIndex, "seg", segIndex, "type", outType.Name())
	parser := newAvpipeOutParser(o.videoOutCh, o.classifier, o.pcrLead, o.cbr)
	return &videoOutput{parser: parser}, nil
}

type videoOutput struct {
	parser *avpipeOutParser
}

func (o *videoOutput) Write(buf []byte) (int, error) {
	o.parser.Parse(buf)
	return len(buf), nil
}

func (o *videoOutput) Seek(offset int64, whence int) (int64, error) { return 0, nil }
func (o *videoOutput) Close() error                                 { return nil }
func (o *videoOutput) Stat(streamIndex int, avType goavpipe.AVType, statType goavpipe.AVStatType, statArgs interface{}) error {
	return nil
}

// avpipeInputURL is the pseudo-URL for avpipe xc - forces use of custom AVIO
const avpipeInputURL = "mpegts-xc-video.ts"

// runAvpipeXc runs avpipe.Xc to transcode the video input
// Blocks until input channel is closed (EOF or error)
func runAvpipeXc(encWidth, encHeight int32, ecodec, dcodec string, videoBitrate int32,
	ch <-chan []byte, videoOutCh chan<- videoPkt, classifier *Classifier, pcrLead int64, cbr bool) error {
	defer close(videoOutCh)
	goavpipe.InitIOHandler(
		&videoInputOpener{ch: ch},
		&videoOutputOpener{videoOutCh: videoOutCh, classifier: classifier, pcrLead: pcrLead, cbr: cbr},
	)

	params := goavpipe.NewXcParams()
	params.Url = avpipeInputURL
	params.XcType = goavpipe.XcVideo
	params.Format = "mpegts" // Produce one single continuous MPEGTS output
	params.EncWidth = encWidth
	params.EncHeight = encHeight
	params.Ecodec = ecodec
	params.Dcodec = dcodec
	params.VideoBitrate = videoBitrate
	// Hard-cap tencoder video bitrate becuase we have to fit the output CBR (can't exceed)
	if videoBitrate > 0 {
		params.RcMaxRate = videoBitrate
		params.RcBufferSize = videoBitrate
	}

	log.Info("mpegts-xc starting avpipe", "url", avpipeInputURL,
		"encWidth", encWidth, "encHeight", encHeight, "ecodec", ecodec, "dcodec", dcodec, "videoBitrate", videoBitrate, "cbr", cbr)
	return avpipe.Xc(params)
}
