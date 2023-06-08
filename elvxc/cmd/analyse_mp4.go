package cmd

import "sync"

type segQueue struct {
	arr   []*hlsSegment
	mutex *sync.RWMutex
	cond  *sync.Cond
}

type mp4Analyser struct {
	queue            *segQueue
	stop             bool
	variantOutputDir string
}

func newMp4Analyser(variantOutputDir string) *mp4Analyser {
	return &mp4Analyser{
		queue:            newSegQueue(),
		variantOutputDir: variantOutputDir,
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
