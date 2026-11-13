package cmd

import (
	"fmt"
	"strings"

	"github.com/eluv-io/avpipe/goavpipe/stamp"
)

// openRenderClient opens the board renderer of --overlay-render, given as "request-fifo,result-fifo": the demo's
// overlay-render in -hash mode, a warm headless Chrome rendering an HTML template. The pictures go into the event
// log as they arrive.
func openRenderClient(spec string, eventLog *stamp.Log) (*stamp.RenderClient, error) {
	paths := strings.Split(spec, ",")
	if len(paths) != 2 {
		return nil, fmt.Errorf("--overlay-render wants request-fifo,result-fifo")
	}
	client, err := stamp.OpenRenderClient(paths[0], paths[1], func(hash string, png []byte, err error) {
		if err != nil {
			log.Error("overlay render failed", "hash", hash, "err", err)
			return
		}
		eventLog.PutAsset(hash, png)
		log.Debug("overlay board rendered", "hash", hash, "bytes", len(png))
	})
	if err != nil {
		return nil, err
	}
	eventLog.SetAssetFetcher(func(string) {}) // a picture not in the log yet is on its way: the job waits
	return client, nil
}
