package avpipe_test

import (
	"io"
	"sync"
	"testing"

	. "github.com/qluvio/avpipe"
)

const str1 string = "This is a text simulating the input of the AVPipe"
const expectedReader1 int = 7000

/* Implements AVPipeOpener */
type MyOpener struct {
	event chan (io.ReadCloser)
}

func (o MyOpener) Open(t AVType, segno int) (io.WriteCloser, error) {
	pr, pw := io.Pipe()
	o.event <- pr
	return pw, nil
}

func drainSingleOutputSingleWrite(t *testing.T, o *MyOpener) {

	/* Wait for a writer to be open */
	select {
	case r := <-o.event:
		buf := make([]byte, 1024)
		rd, err := r.Read(buf)
		if err != nil || rd != len(str1) || string(buf[:rd]) != str1 {
			t.Fail()
		}
		r.Close()
	}
}

func drainReader(t *testing.T, r io.ReadCloser) (int, error) {
	defer r.Close()

	br := 0 /* total bytes read */
	buf := make([]byte, 1024)

	for {
		rd, err := r.Read(buf)
		if err == io.EOF {
			return br, nil
		}
		if err != nil {
			return br, err
		}
		br = br + rd
	}
}

func drainMultiOutput(t *testing.T, o *MyOpener, wg *sync.WaitGroup) {

	for {
		/* Wait for a writer to be open */
		select {
		case r := <-o.event:
			if r == nil {
				wg.Done()
				return
			}
			rd, err := drainReader(t, r)
			if err != nil {
				t.Fail()
			}
			if rd != expectedReader1 {
				t.Fail()
			}
			wg.Done()
		}
	}
}

func TestSingleOutputSingleWrite(t *testing.T) {

	/* Create the source reader */
	sr, sw := io.Pipe()
	defer sr.Close()

	/* Create the output 'opener' */
	o := &MyOpener{}
	o.event = make(chan io.ReadCloser, 1)

	params := AVParams{
		"segment_duration": "2000",
		"start_segment":    "0",
	}

	avp, err := NewAVPipe(sr, o, &params)
	if err != nil {
		t.Fail()
	}
	defer avp.Close()

	/* Wait for output and read it once available */
	go drainSingleOutputSingleWrite(t, o)

	/* Write something to input stream */
	go func() {
		sw.Write([]byte(str1))
		sw.Close()
	}()

	err = avp.Run()
	if err != nil {
		t.Fail()
	}
}

func TestMultiOutput(t *testing.T) {

	/* Create the source reader */
	sr, sw := io.Pipe()
	defer sr.Close()

	/* Create the output 'opener' */
	o := &MyOpener{}
	o.event = make(chan io.ReadCloser, 1)

	params := AVParams{
		"segment_duration":  "2000",
		"start_segment":     "0",
		"test_segment_size": "7000",
	}

	wg := &sync.WaitGroup{}
	wg.Add(4 + 1)

	avp, err := NewAVPipe(sr, o, &params)
	if err != nil {
		t.Fail()
	}
	defer avp.Close()

	/* Wait for output and read it once available */
	go drainMultiOutput(t, o, wg)

	/* Write 4 buffers to the input stream */
	buf := make([]byte, 10240)
	go func() {
		sw.Write(buf[:7000])
		sw.Write(buf[:7000])
		sw.Write(buf[:7000])
		sw.Write(buf[:7000])
		sw.Close()
	}()

	err = avp.Run()
	if err != nil && err != io.EOF {
		t.Fail()
	}
	o.event <- nil

	wg.Wait()
}
