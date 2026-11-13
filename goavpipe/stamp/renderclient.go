package stamp

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// RenderClient asks an external board renderer for pictures. A request is one JSON line {"hash", "state"} on the
// request file; the renderer answers on the result file with {"hash", "png"} lines, the picture base64-encoded, or
// {"hash", "error"}. The two files are usually FIFOs and are opened read-write, so neither side waits for the
// other to start. The renderer is a process of its own, which keeps the browser that draws the template out of
// the caller.
type RenderClient struct {
	deliver func(hash string, png []byte, err error)
	mu      sync.Mutex
	req     *os.File
	res     *os.File
}

type renderRequest struct {
	Hash  string         `json:"hash"`
	State map[string]any `json:"state"`
}

type renderResult struct {
	Hash string `json:"hash"`
	PNG  string `json:"png"`
	Err  string `json:"error,omitempty"`
}

// OpenRenderClient opens the request and result files and starts reading results. deliver is called for every
// answer, from one goroutine: with the picture, or with the error the renderer reported for the hash. An answer
// that cannot be read is delivered with an empty hash and the error. deliver must not block for long.
func OpenRenderClient(requests, results string, deliver func(hash string, png []byte, err error)) (*RenderClient, error) {
	req, err := os.OpenFile(requests, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("render requests %s: %w", requests, err)
	}
	res, err := os.OpenFile(results, os.O_RDWR, 0)
	if err != nil {
		req.Close()
		return nil, fmt.Errorf("render results %s: %w", results, err)
	}
	c := &RenderClient{deliver: deliver, req: req, res: res}
	go c.read()
	return c, nil
}

// Render asks for the picture of state, the keys a template binding shows, named hash (Bindings.Render gives
// both). The answer arrives through deliver. Safe from any goroutine.
func (c *RenderClient) Render(hash string, state map[string]any) error {
	b, err := json.Marshal(renderRequest{Hash: hash, State: state})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = fmt.Fprintf(c.req, "%s\n", b)
	return err
}

func (c *RenderClient) read() {
	scanner := bufio.NewScanner(c.res)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r renderResult
		if err := json.Unmarshal(line, &r); err != nil {
			c.deliver("", nil, fmt.Errorf("render result: %w", err))
			continue
		}
		if r.Err != "" {
			c.deliver(r.Hash, nil, fmt.Errorf("render failed: %s", r.Err))
			continue
		}
		png, err := base64.StdEncoding.DecodeString(r.PNG)
		if err != nil {
			c.deliver(r.Hash, nil, fmt.Errorf("render result is not base64: %w", err))
			continue
		}
		c.deliver(r.Hash, png, nil)
	}
}

// Close closes both files, which ends the reading goroutine.
func (c *RenderClient) Close() error {
	err := c.req.Close()
	if err2 := c.res.Close(); err == nil {
		err = err2
	}
	return err
}
