package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type segQueue struct {
	arr   []*hlsSegment
	mutex *sync.RWMutex
	cond  *sync.Cond
}

type mp4Analyser struct {
	queue                     *segQueue
	stop                      bool
	variantOutputDir          string
	baseUrl                   string
	lastDiscontinuitySequence int
	prevHlsManifest           *parsedHlsManifest // previous HLS manifest
	savedSegIndex             int                // starts from 1 and will be incremented every time an ABR segment is saved
}

func newMp4Analyser(url, variantOutputDir string) *mp4Analyser {
	index := strings.LastIndex(url, "/")
	return &mp4Analyser{
		queue:            newSegQueue(),
		variantOutputDir: variantOutputDir,
		baseUrl:          url[:index+1],
	}
}

func (this *mp4Analyser) push(hlsManifest *parsedHlsManifest) {

	if this.prevHlsManifest == nil {
		this.prevHlsManifest = hlsManifest
		return
	}

	prevDiscontinuitySequence := 0
	if _, ok := this.prevHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"]; ok {
		prevDiscontinuitySequence = this.prevHlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
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
		this.saveSegment(prevDiscontinuityDir, &hlsManifest.segments[j])
		i++
	}

	this.prevHlsManifest = hlsManifest
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

	segInfo := filepath.Join(dir, segNameInfo)
	os.WriteFile(segInfo, []byte(segment.uri), 0644)

	return nil
}

func (this *mp4Analyser) buildAbrSegmentUrl(segUrl string) string {
	newUrl := this.baseUrl + segUrl
	return newUrl
}

func newSegQueue() *segQueue {
	q := &segQueue{
		mutex: &sync.RWMutex{},
	}

	q.cond = sync.NewCond(q.mutex)
	return q
}

func (q *segQueue) enqueue(seg *hlsSegment) {
	q.mutex.Lock()
	defer q.mutex.Unlock()
	q.arr = append(q.arr, seg)
}

func (q *segQueue) dequeue() *hlsSegment {
	q.mutex.Lock()
	for len(q.arr) == 0 {
		q.cond.Wait()
	}

	seg := q.arr[0]
	q.arr = q.arr[1:]
	q.mutex.Unlock()
	return seg
}
