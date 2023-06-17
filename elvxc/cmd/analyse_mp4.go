package cmd

import (
	"bytes"
	"fmt"
	mp4 "github.com/abema/go-mp4"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type manifestQueue struct {
	arr   []*parsedHlsManifest
	mutex *sync.RWMutex
	cond  *sync.Cond
}

type mp4Analyser struct {
	queue                     *manifestQueue
	stop                      bool
	variantOutputDir          string
	baseUrl                   string
	lastDiscontinuitySequence int
	lastSequenceNo            uint32             // Last sequence number of prev m4s segment
	prevHlsManifest           *parsedHlsManifest // previous HLS manifest
	savedSegIndex             int                // starts from 1 and will be incremented every time an ABR segment is saved
}

type Fragment struct {
	sequenceNo uint32
	duration   uint32
	pts        uint64
	dts        uint64
	timescale  uint32
}

func newMp4Analyser(url, variantOutputDir string) *mp4Analyser {
	index := strings.LastIndex(url, "/")
	return &mp4Analyser{
		queue:            newManifestQueue(),
		variantOutputDir: variantOutputDir,
		baseUrl:          url[:index+1],
	}
}

func (this *mp4Analyser) Start() {
	go func() {
		for !this.stop {
			hlsManifest := this.queue.dequeue()

			if this.prevHlsManifest == nil {
				this.prevHlsManifest = hlsManifest
				continue
			}

			prevDiscontinuitySequence := 0
			if _, ok := this.prevHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"]; ok {
				prevDiscontinuitySequence = this.prevHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
			}

			curDiscontinuitySequence := 0
			if _, ok := hlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"]; ok {
				curDiscontinuitySequence = hlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
			}

			prevSequenceNumber := 0
			if _, ok := this.prevHlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"]; ok {
				prevSequenceNumber = this.prevHlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"].(int)
			}

			prevDiscontinuityDir := filepath.Join(this.variantOutputDir, fmt.Sprintf("disc-%d", prevDiscontinuitySequence))
			os.Mkdir(prevDiscontinuityDir, os.ModePerm)

			sequenceNumber := hlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"].(int)

			j := 0
			for i := 0; i < sequenceNumber-prevSequenceNumber; j++ {
				log.Debug("XXX", "sequenceNumber", sequenceNumber, "prevSequenceNumber", prevSequenceNumber, "i", i, "j", j, "segment", hlsManifest.segments[j])
				if hlsManifest.segments[j].isDiscontinuityTag {
					continue
				}
				if hlsManifest.segments[j].isInit {
					this.saveSegment(prevDiscontinuityDir, &this.prevHlsManifest.segments[j])
					continue
				}
				this.saveSegment(prevDiscontinuityDir, &this.prevHlsManifest.segments[j])
				i++
			}

			this.prevHlsManifest = hlsManifest

			// Reset lastSequenceNo if the discontinuity sequence number is changed.
			if this.lastDiscontinuitySequence != curDiscontinuitySequence {
				this.lastSequenceNo = 0
			}

			this.lastDiscontinuitySequence = curDiscontinuitySequence
		}
	}()
}

func (this *mp4Analyser) Stop() {
	this.stop = true
}

func (this *mp4Analyser) Push(hlsManifest *parsedHlsManifest) {
	this.queue.enqueue(hlsManifest)
}

func (this *mp4Analyser) saveSegment(dir string, segment *hlsSegment) error {
	segUri := this.baseUrl + segment.uri
	log.Debug("saveSegment", "segUri", segUri)
	resp, err := http.Get(segUri)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)

	var segName string
	var segNameInfo string
	if segment.isInit {
		segName = "init.m4s"
	} else {
		segName = fmt.Sprintf("%d.m4s", this.savedSegIndex)
		segNameInfo = fmt.Sprintf("%d.info", this.savedSegIndex)
		this.savedSegIndex++
	}

	segPath := filepath.Join(dir, segName)
	os.WriteFile(segPath, body, 0644)

	this.analyseMp4Boxes(body)

	segInfo := filepath.Join(dir, segNameInfo)
	os.WriteFile(segInfo, []byte(segment.uri), 0644)

	return nil
}

func (this *mp4Analyser) analyseMp4Boxes(segment []byte) {

	r := bytes.NewReader(segment)

	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{{mp4.BoxTypeMoof(), mp4.BoxTypeMfhd()}, {mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfhd()}, {mp4.BoxTypeMoof(), mp4.BoxTypeTraf(), mp4.BoxTypeTfdt()}, {mp4.BoxTypeSidx()}})
	if err != nil {
		fmt.Printf("Failed to extract err=%v\n", err)
		return
	}

	var fragments []*Fragment
	var fragment *Fragment

	for _, box := range boxes {
		sidx, ok := box.Payload.(*mp4.Sidx)
		if ok {
			fragment = &Fragment{
				pts:       sidx.EarliestPresentationTimeV1,
				timescale: sidx.Timescale,
			}
			continue
		}

		mfhd, ok := box.Payload.(*mp4.Mfhd)
		if ok {
			fragment.sequenceNo = mfhd.SequenceNumber
			continue
		}

		tfhd, ok := box.Payload.(*mp4.Tfhd)
		if ok {
			fragment.duration = tfhd.DefaultSampleDuration
			continue
		}

		tfdt, ok := box.Payload.(*mp4.Tfdt)
		if ok {
			fragment.dts = tfdt.BaseMediaDecodeTimeV1
		}

		fragments = append(fragments, fragment)
	}

	// Check first sequenceNo with last seen sequenceNo from prev segment
	if this.lastSequenceNo != 0 && len(fragments) > 0 {
		if fragments[0].sequenceNo < this.lastSequenceNo {
			fmt.Printf("ERROR: last segment sequence number (%d) is smaller than starting sequence number of current segment (%d)\n",
				this.lastSequenceNo, fragments[0].sequenceNo)
		}
	}

	for i := 0; i < len(fragments)-1; i++ {
		if fragments[i].sequenceNo+1 != fragments[i+1].sequenceNo {
			fmt.Printf("WARNING: sequence number is not consequtive from frame %d to frame %d\n", i, i+1)
		}

		if fragments[i].pts >= fragments[i+1].pts {
			fmt.Printf("ERROR: frame %d has pts %d > frame %d pts %d\n", i, fragments[i].pts, i+1, fragments[i+1].pts)
		}

		if fragments[i].dts >= fragments[i+1].dts {
			fmt.Printf("ERROR: frame %d has dts %d > frame %d dts %d\n", i, fragments[i].dts, i+1, fragments[i+1].dts)
		}
	}

	if len(fragments) > 0 {
		this.lastSequenceNo = fragments[len(fragments)-1].sequenceNo
	}
}

func (this *mp4Analyser) buildAbrSegmentUrl(segUrl string) string {
	newUrl := this.baseUrl + segUrl
	return newUrl
}

func newManifestQueue() *manifestQueue {
	q := &manifestQueue{
		mutex: &sync.RWMutex{},
	}

	q.cond = sync.NewCond(q.mutex)
	return q
}

func (q *manifestQueue) enqueue(hlsManifest *parsedHlsManifest) {
	q.mutex.Lock()
	q.arr = append(q.arr, hlsManifest)
	q.cond.Broadcast()
	defer q.mutex.Unlock()
}

func (q *manifestQueue) dequeue() *parsedHlsManifest {
	q.mutex.Lock()
	for len(q.arr) == 0 {
		q.cond.Wait()
	}

	hlsManifest := q.arr[0]
	q.arr = q.arr[1:]
	q.mutex.Unlock()
	return hlsManifest
}
