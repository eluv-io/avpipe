package cmd

import (
	"fmt"
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
	lastSequenceNumber        int
	lastDiscontinuitySequence int
	lastHlsManifest           *parsedHlsManifest
}

func newMp4Analyser(url, variantOutputDir string) *mp4Analyser {
	index := strings.LastIndex(url, "/")
	return &mp4Analyser{
		queue:            newSegQueue(),
		variantOutputDir: variantOutputDir,
		baseUrl:          url[:index+1],
	}
}

func (this *mp4Analyser) start() {
	for !this.stop {
		segment := this.queue.dequeue()
		this.saveSegment(segment)
	}
}

func (this *mp4Analyser) saveSegment(segment *hlsSegment) {
	if len(this.variantOutputDir) == 0 {
		return
	}
}

func (this *mp4Analyser) push(hlsManifest *parsedHlsManifest) {

	discontinuitySequence := hlsManifest.headers["#EXT-X-DISCONTINUITY-SEQUENCE"].(int)
	discontinuityDir := filepath.Join(this.variantOutputDir, fmt.Sprintf("disc-%d", discontinuitySequence))
	os.Mkdir(discontinuityDir, os.ModePerm)

	sequenceNumber := hlsManifest.headers["#EXT-X-MEDIA-SEQUENCE"].(int)

	j := 0
	for i := 0; i < sequenceNumber-this.lastSequenceNumber; j++ {
		if hlsManifest.segments[j].isInit {
			saveSegment(discontinuityDir, "init.m4s")
		}
	}

}

func saveSegment()

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
