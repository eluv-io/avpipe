package cmd

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/eluv-io/avpipe/goavpipe"
	"github.com/eluv-io/avpipe/goavpipe/stamp"
)

// renderClient talks to an external board renderer over two FIFOs: a request per new board state goes out as a
// JSON line {"hash", "state"}, and the renderer answers {"hash", "png"} lines with the picture base64-encoded,
// which go into the event log as assets. The renderer is the demo's overlay-render in -hash mode, a warm headless
// Chrome rendering an HTML template; keeping it a separate process keeps Chrome out of elvxc.
type renderClient struct {
	log *stamp.Log
	mu  sync.Mutex
	req *os.File
	res *os.File
}

type renderRequest struct {
	Hash  string           `json:"hash"`
	State stamp.BoardState `json:"state"`
}

type renderResult struct {
	Hash string `json:"hash"`
	PNG  string `json:"png"`
	Err  string `json:"error,omitempty"`
}

// openRenderClient opens "req.fifo,res.fifo" read-write, as the other FIFOs are, and starts reading results.
func openRenderClient(spec string, log *stamp.Log) (*renderClient, error) {
	paths := strings.Split(spec, ",")
	if len(paths) != 2 {
		return nil, fmt.Errorf("--overlay-render wants request-fifo,result-fifo")
	}
	req, err := os.OpenFile(paths[0], os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("render requests %s: %w", paths[0], err)
	}
	res, err := os.OpenFile(paths[1], os.O_RDWR, 0)
	if err != nil {
		req.Close()
		return nil, fmt.Errorf("render results %s: %w", paths[1], err)
	}
	c := &renderClient{log: log, req: req, res: res}
	log.SetAssetFetcher(func(string) {}) // a picture not in the log yet is on its way: the job waits
	go c.read()
	return c, nil
}

// render is the RenderMapping's callback: one request per new board state.
func (c *renderClient) render(hash string, state stamp.BoardState) {
	b, _ := json.Marshal(renderRequest{Hash: hash, State: state})
	c.mu.Lock()
	_, err := fmt.Fprintf(c.req, "%s\n", b)
	c.mu.Unlock()
	if err != nil {
		log.Error("overlay render request failed", "err", err, "hash", hash)
	}
}

func (c *renderClient) read() {
	scanner := bufio.NewScanner(c.res)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r renderResult
		if err := json.Unmarshal(line, &r); err != nil {
			log.Error("bad render result", "err", err)
			continue
		}
		if r.Err != "" {
			log.Error("overlay render failed", "hash", r.Hash, "err", r.Err)
			continue
		}
		png, err := base64.StdEncoding.DecodeString(r.PNG)
		if err != nil {
			log.Error("render result is not base64", "hash", r.Hash, "err", err)
			continue
		}
		c.log.PutAsset(r.Hash, png)
		log.Debug("overlay board rendered", "hash", r.Hash, "bytes", len(png))
	}
}

func (c *renderClient) close() {
	c.req.Close()
	c.res.Close()
}

// renderMappingFor picks the layout's slots for a RenderMapping: the image slot named board, or the first image
// slot without a default, and the clock slots named clock and shot when present.
func renderMappingFor(layout *goavpipe.OverlayParams, render func(string, stamp.BoardState)) (*stamp.RenderMapping, error) {
	board := ""
	for _, s := range layout.Slots {
		if s.Type == goavpipe.OverlaySlotImage && (s.Name == "board" || board == "" && s.Default == "") {
			board = s.Name
		}
	}
	if board == "" {
		return nil, fmt.Errorf("--overlay-render needs an image slot for the board (named board, or without a default)")
	}
	clockSlot := func(name string) string {
		if s := layout.Slot(name); s != nil && s.Type == goavpipe.OverlaySlotClock {
			return name
		}
		return ""
	}
	return stamp.NewRenderMapping(board, clockSlot("clock"), clockSlot("shot"), render), nil
}
