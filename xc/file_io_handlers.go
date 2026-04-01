// file_io_handlers.go provides local-file implementations of the avpipe I/O
// interfaces (goavpipe.InputOpener / OutputOpener) for use in tests.
//
// Special test features:
//
//   - Error injection: ErrorOnOpen / ErrorOnRead on FileInputOpener force
//     deterministic I/O failures
//
//   - Stat collection: when a shared IOStats is attached, the handlers
//     accumulate transcoding stats (frames read/written, keyframe PTS,
//     encoding stats) reported by the avpipe xc stats callbacks.
package xc

import (
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/eluv-io/avpipe"
	"github.com/eluv-io/avpipe/goavpipe"
)

// IOStats collects stats from the avpipe C engine during transcoding.
// Attach to FileInputOpener and/or FileOutputOpener to capture stats.
type IOStats struct {
	AudioFramesRead         uint64
	VideoFramesRead         uint64
	FirstKeyFramePTS        uint64
	EncodingAudioFrameStats avpipe.EncodingFrameStats
	EncodingVideoFrameStats avpipe.EncodingFrameStats
}

// FileInputOpener implements goavpipe.InputOpener for local files.
type FileInputOpener struct {
	URL         string
	ErrorOnOpen bool     // return fs.ErrPermission from Open
	ErrorOnRead bool     // return io.ErrNoProgress from Read
	Stats       *IOStats // optional stats collector
}

func (fio *FileInputOpener) Open(_ int64, url string) (goavpipe.InputHandler, error) {
	if fio.ErrorOnOpen {
		return nil, fs.ErrPermission
	}

	f, err := os.Open(url)
	if err != nil {
		return nil, err
	}
	fio.URL = url
	return &fileInput{
		file:        f,
		errorOnRead: fio.ErrorOnRead,
		stats:       fio.Stats,
	}, nil
}

type fileInput struct {
	file        *os.File
	errorOnRead bool
	stats       *IOStats
}

func (i *fileInput) Read(buf []byte) (int, error) {
	n, err := i.file.Read(buf)
	if err == io.EOF {
		return 0, nil
	}
	if i.errorOnRead {
		return -1, io.ErrNoProgress
	}
	return n, err
}

func (i *fileInput) Seek(offset int64, whence int) (int64, error) {
	return i.file.Seek(offset, whence)
}

func (i *fileInput) Close() error {
	return i.file.Close()
}

func (i *fileInput) Size() int64 {
	fi, err := i.file.Stat()
	if err != nil {
		return -1
	}
	return fi.Size()
}

func (i *fileInput) Stat(_ int, statType goavpipe.AVStatType, statArgs interface{}) error {
	if i.stats == nil {
		return nil
	}
	switch statType {
	case goavpipe.AV_IN_STAT_AUDIO_FRAME_READ:
		i.stats.AudioFramesRead = *statArgs.(*uint64)
	case goavpipe.AV_IN_STAT_VIDEO_FRAME_READ:
		i.stats.VideoFramesRead = *statArgs.(*uint64)
	case goavpipe.AV_IN_STAT_FIRST_KEYFRAME_PTS:
		i.stats.FirstKeyFramePTS = *statArgs.(*uint64)
	}
	return nil
}

// FileOutputOpener implements goavpipe.OutputOpener for local files.
type FileOutputOpener struct {
	Dir   string
	Stats *IOStats // optional stats collector
}

func (oo *FileOutputOpener) Open(_, _ int64, streamIndex, segIndex int,
	pts int64, outType goavpipe.AVType) (goavpipe.OutputHandler, error) {

	var filename string

	switch outType {
	case goavpipe.DASHVideoInit:
		filename = fmt.Sprintf("./%s/vinit-stream%d.m4s", oo.Dir, streamIndex)
	case goavpipe.DASHAudioInit:
		filename = fmt.Sprintf("./%s/ainit-stream%d.m4s", oo.Dir, streamIndex)
	case goavpipe.DASHManifest:
		filename = fmt.Sprintf("./%s/dash.mpd", oo.Dir)
	case goavpipe.DASHVideoSegment:
		filename = fmt.Sprintf("./%s/vchunk-stream%d-%05d.m4s", oo.Dir, streamIndex, segIndex)
	case goavpipe.DASHAudioSegment:
		filename = fmt.Sprintf("./%s/achunk-stream%d-%05d.m4s", oo.Dir, streamIndex, segIndex)
	case goavpipe.HLSMasterM3U:
		filename = fmt.Sprintf("./%s/master.m3u8", oo.Dir)
	case goavpipe.HLSVideoM3U, goavpipe.HLSAudioM3U:
		filename = fmt.Sprintf("./%s/media_%d.m3u8", oo.Dir, streamIndex)
	case goavpipe.AES128Key:
		filename = fmt.Sprintf("./%s/key.bin", oo.Dir)
	case goavpipe.MP4Segment:
		filename = fmt.Sprintf("./%s/segment-%d.mp4", oo.Dir, segIndex)
	case goavpipe.FMP4VideoSegment:
		filename = fmt.Sprintf("./%s/vsegment-%d.mp4", oo.Dir, segIndex)
	case goavpipe.FMP4AudioSegment:
		filename = fmt.Sprintf("./%s/asegment%d-%d.mp4", oo.Dir, streamIndex, segIndex)
	case goavpipe.FrameImage:
		filename = fmt.Sprintf("./%s/%d.jpeg", oo.Dir, pts)
	}

	f, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return nil, err
	}
	return &fileOutput{file: f, stats: oo.Stats}, nil
}

type fileOutput struct {
	file  *os.File
	stats *IOStats
}

func (o *fileOutput) Write(buf []byte) (int, error) {
	return o.file.Write(buf)
}

func (o *fileOutput) Seek(offset int64, whence int) (int64, error) {
	return o.file.Seek(offset, whence)
}

func (o *fileOutput) Close() error {
	return o.file.Close()
}

func (o *fileOutput) Stat(_ int, avType goavpipe.AVType, statType goavpipe.AVStatType, statArgs interface{}) error {
	if o.stats == nil {
		return nil
	}
	if statType == goavpipe.AV_OUT_STAT_FRAME_WRITTEN {
		es := statArgs.(*avpipe.EncodingFrameStats)
		if avType == goavpipe.FMP4AudioSegment {
			o.stats.EncodingAudioFrameStats = *es
		} else {
			o.stats.EncodingVideoFrameStats = *es
		}
	}
	return nil
}
