package live

import (
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/grafov/m3u8"

	"eluvio/errors"
	elog "eluvio/log"
)

var log = elog.Get("/eluvio/avpipe/live")

// HLSReader provides a reader interface to a live HLS stream - it reads a
//   specified number of segments on outputs one aggregate MPEG-TS buffer
type HLSReader struct {
	sequence          int
	client            http.Client
	url               *url.URL
	numSegmentsNeeded int
	numSegmentsRead   int
}

// NewHLSReader ...
func NewHLSReader(url *url.URL) *HLSReader {
	lhr := HLSReader{
		sequence: -1,
		client:   http.Client{},
		url:      url,
	}
	return &lhr
}

func (lhr *HLSReader) openURL(u *url.URL) (io.ReadCloser, error) {
	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := lhr.client.Do(req)
	if err != nil {
		return nil, err
	} else if resp.StatusCode != 200 {
		return nil, errors.E("AVLR HTTP GET failed", "status", resp.StatusCode, "URL", u.String())
	}

	return resp.Body, nil
}

// resolve returns an absolute URL
func resolve(urlStr string, base *url.URL) (*url.URL, error) {
	url, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}

	if url.IsAbs() {
		return url, nil
	}

	return base.ResolveReference(url), nil
}

func (lhr *HLSReader) saveToFile(u *url.URL) error {
	fileName := path.Base(u.Path)

	out, err := os.Create(fileName)
	if err != nil {
		return err
	}
	defer out.Close()

	content, err := lhr.openURL(u)
	if err != nil {
		return err
	}
	defer content.Close()

	_, err = io.Copy(out, content)
	if err != nil {
		return err
	}

	log.Info("AVLR saved file", fileName)

	return nil
}

func (lhr *HLSReader) readSegment(u *url.URL, w io.Writer) (written int64, err error) {
	log.Info("AVLR readSegment start", "u", u)
	t := time.Now()
	content, err := lhr.openURL(u)
	if content != nil {
		defer content.Close()
	}
	if err != nil {
		return 0, err
	}

	written, err = io.Copy(w, content)
	log.Info("AVLR readSegment end", "written", written, "err", err, "timeSpent", time.Since(t))
	return
}

func (lhr *HLSReader) readMasterPlaylist(u *url.URL) ([]*m3u8.Variant, error) {
	content, err := lhr.openURL(u)
	if content != nil {
		defer content.Close()
	}
	if err != nil {
		return nil, err
	}

	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		return nil, err
	}

	if listType != m3u8.MASTER {
		return nil, errors.E("AVLR invalid playlist")
	}

	masterPlaylist := playlist.(*m3u8.MasterPlaylist)
	return masterPlaylist.Variants, nil
}

// readPlaylist HTTP GET the HLS media playlist and process segments up to
// durationSec . Start at sequence number (startSeqNo) specified at offset
// startSec. Return the next sequence number and offset, essentially where
// to continue next time. durationReadSec is the length of video processed.
//
// PENDING(PT) - ideally we can control this with something more precise than duration
func (lhr *HLSReader) readPlaylist(u *url.URL, startSeqNo int,
	startSec float64, durationSec float64, w io.Writer) (
	nextSeqNo int, nextStartSec float64, durationReadSec float64, err error) {

	log.Debug("AVLR readPlaylist start", "startSeqNo", startSeqNo, "startSec", startSec, "durationSec", durationSec)
	content, err := lhr.openURL(u)
	if err != nil {
		return startSeqNo, startSec, 0, err
	}
	defer content.Close()

	playlist, listType, err := m3u8.DecodeFrom(content, true)
	if err != nil {
		return startSeqNo, startSec, 0, err
	} else if listType != m3u8.MEDIA {
		return startSeqNo, startSec, 0,
			errors.E("AVLR unexpected playlist type", "listType", listType)
	}

	mediaPlaylist := playlist.(*m3u8.MediaPlaylist)

	startIndex := -1
	durationToEdge := float64(0)
	edgeSeqNo := 0
	for i, segment := range mediaPlaylist.Segments {
		if segment == nil {
			// No more segments
			if startSeqNo == -1 {
				// First segment in the recording
				startSeqNo = edgeSeqNo
				startIndex = i - 1
				log.Info("AVLR initializing recording at live edge", "seqNo", edgeSeqNo)
			} else if startIndex == -1 {
				// Segment not found in the playlist
				if startSeqNo < edgeSeqNo {
					startSeqNo = edgeSeqNo
					startIndex = i - 1
					log.Error("AVLR fell too far behind live edge, skipping ahead", "seqNo", edgeSeqNo)
				} else {
					log.Debug("AVLR no new segments available")
					return startSeqNo, startSec, 0, nil
				}
			}
			break
		}
		edgeSeqNo = int(segment.SeqId)
		if startSeqNo == edgeSeqNo {
			startIndex = i
		} else if startSeqNo < edgeSeqNo && startSeqNo != -1 {
			durationToEdge += segment.Duration
		}
	}
	if durationToEdge > 0 {
		log.Warn("AVLR falling behind live edge", "durationToEdge", durationToEdge, "edgeSeqNo", edgeSeqNo)
	}

	nextSeqNo = startSeqNo
	for i := startIndex; ; i++ {
		segment := mediaPlaylist.Segments[i]
		if segment == nil {
			break
		}

		msURL, err := resolve(segment.URI, u)
		if err != nil {
			log.Error("AVLR Failed to resolve segment URL", "err", err, "segment.URI", segment.URI)
			continue // nextSeqNo will fix itself in the following section
		}

		// Assert of sorts
		if nextSeqNo != int(segment.SeqId) {
			log.Warn("AVLR nextSeqNo should equal segment.SeqId", "nextSeqNo", nextSeqNo, "segment.SeqId", segment.SeqId)
			nextSeqNo = int(segment.SeqId)
		}

		segRemainingSec := durationSec - durationReadSec
		log.Info("AVLR processing ingest segment", "seqNo", nextSeqNo, "segment.Duration", segment.Duration, "segRemainingSec",
			segRemainingSec, "durationReadSec", durationReadSec, "URI", segment.URI)

		// lhr.saveToFile(msURL)
		written, err := lhr.readSegment(msURL, w)
		if err != nil {
			// Transcoded part of a segment - ErrClosedPipe when avpipe closes the pipe
			if !(err == io.EOF || err == io.ErrClosedPipe) || segment.Duration < segRemainingSec {
				log.Error("AVLR failed to read requested duration", "written", written, "err", err,
					"nextSeqNo", nextSeqNo, "nextStartSec", 0, "durationReadSec", durationReadSec)
				return nextSeqNo, 0, durationReadSec, err
			}
			log.Info("AVLR successfully read requested duration, ending with a partial segment", "written", written, "err", err,
				"nextSeqNo", nextSeqNo, "nextStartSec", segRemainingSec, "durationReadSec", durationSec)
			return nextSeqNo, segRemainingSec, durationSec, nil
		}

		durationReadSec += segment.Duration - startSec
		startSec = 0
		nextSeqNo++
		if durationReadSec >= durationSec {
			log.Info("AVLR successfully read requested duration", "nextSeqNo", nextSeqNo, "nextStartSec", 0, "durationReadSec", durationSec)
			if durationReadSec > durationSec {
				log.Warn("AVLR read more than requested duration", "durationSec", durationSec, "durationReadSec", durationReadSec)
			}
			return nextSeqNo, 0, durationSec, nil
		}
	}

	log.Info("AVLR read all available segments", "nextSeqNo", nextSeqNo, "nextStartSec", startSec, "durationReadSec", durationReadSec)
	return nextSeqNo, startSec, durationReadSec, nil
}

// Fill reads HLS input as indicated by parameters startSesquence and numSegments
// and writes it out to the provided io.Writer
// If startSeqNo is -1, it starts with the first sequence it gets
// The sequence number is 0-based (i.e. the first segment has sequence number 0)
func (lhr *HLSReader) Fill(startSeqNo int, startSec float64, durationSec float64, w io.Writer) (
	nextSeqNo int, nextStartSec float64, err error) {

	log.Info("AVLR Fill start", "startSeqNo", startSeqNo, "startSec", startSec, "durationSec", durationSec, "playlist", lhr.url)

	variants, err := lhr.readMasterPlaylist(lhr.url)
	if err != nil {
		return startSeqNo, startSec, err
	}

	var msURL *url.URL
	for _, variant := range variants {
		if variant == nil {
			log.Warn("AVLR skipping invalid variant (nil)")
			continue
		}
		// PENDING(SSS) - check if we have multiple variants and if so only
		// read the one we are supposed to

		msURL, err = resolve(variant.URI, lhr.url)
		if err != nil {
			log.Error("AVLR failed to resolve URL", "err", err, "url", lhr.url)
		} else {
			break
		}
	}
	if msURL == nil {
		return startSeqNo, startSec, errors.E("AVLR media playlist not found")
	}
	log.Info("AVLR media playlist found", "URL", msURL)

	for {
		var durationReadSec float64
		nextSeqNo, nextStartSec, durationReadSec, err =
			lhr.readPlaylist(msURL, startSeqNo, startSec, durationSec, w)
		if err != nil {
			log.Error("AVLR failed to read playlist", "nextSeqNo", nextSeqNo, "nextStartSec", nextStartSec, "err", err)
			return
		}
		// log.Debug("AVLR readPlaylist returned", "nextSeqNo", nextSeqNo, "nextStartSec", nextStartSec, "durationReadSec", durationReadSec, "err", err)

		if durationReadSec >= durationSec {
			log.Info("AVLR Fill done", "nextSeqNo", nextSeqNo, "nextStartSec", nextStartSec, "err", err)
			return
		} else if durationReadSec > 0 {
			startSeqNo = nextSeqNo
			startSec = nextStartSec
			durationSec -= durationReadSec
		} else {
			// Wait for a new segment - typically segments are 2 sec or longer
			// PENDING(PT) HLS spec says to base the wait time on segment duration
			time.Sleep(time.Duration(1000 * time.Millisecond))
		}
	}
}
