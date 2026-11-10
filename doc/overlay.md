# Overlay: timed events into a transcode

avpipe can burn a graphic into the output video that changes on exact frames: a scoreboard whose score, clock
and pictures come from a feed or an API. This page is the map for someone wiring it into a recorder or playout
service, or testing it. The design notes behind it are outside the repo.

## The pieces

| Piece | Where | What it is |
|---|---|---|
| Layout | `goavpipe.OverlayParams` | The slots (text, clock, image) with positions as fractions of the frame, the static image library, the timescale of event pts, and the timing policy `WaitMs` / `OnTimeout` |
| Events | `goavpipe.OverlayEvent` | A full snapshot of every slot from its pts on: `SlotValue` per slot, one of text, image (by library name or by hash), clock state (`Ms` at the event's pts, `Running`, `Up`) or hide |
| Source | `goavpipe.EventSource` on `XcParams.Events` | The job's one source of events, for every kind: `Kinds()`, `At(kind, pts)`, `Asset(hash)`, `Close()`. Every call answers at once with the value, *not yet* or *ended* |
| Static source | `goavpipe.StaticSource(events, assets, kinds...)` | A source over events known up front: VOD, tests, elvxc spec files |
| Compositor | `libavpipe/src/avpipe_overlay.c`, bridge `events.go` and `overlay.go` | Holds one current event per kind, asks the source once per decoded frame, waits in 10 ms slices for *not yet* up to `WaitMs`, then fails (`EAV_OVERLAY`) or holds; draws image slots into one canvas blended in the encoder's pixel format and text and clocks through `drawtext` |
| Stamper core | `goavpipe/stamp` | `Log`: the stamped events of a stream with a seal per kind, per-job views with a pts offset, assets by hash, an event hook for persistence. `ANC`: the ST 2038 venue clock feed (common-go `media/anc/venueclock`) decoded from the `AV_IN_STAT_DATA_ANC` stat into events, with a text mapping (`VenueMapping`) or a rendered-board mapping (`RenderMapping`) |
| ANC stat | `AV_IN_STAT_DATA_ANC`, `goavpipe.AncPacket` | Every ST 2038 PES packet of the input, delivered off the demux thread through a bounded queue; on a live input a consumer that falls behind loses packets and `Dropped` says how many |

Events carry a `Kind`. The overlay is `overlay`; `vertical_crop` is defined (`CropEvent`) for the crop filter to
move onto the same channel.

## Wiring a source

A recorder or playout service does three things:

1. Build the layout and give the job a source. A static one for a finished part:

   ```go
   params.Overlay = layout
   params.Events = goavpipe.StaticSource(events, assets)
   ```

   A live one from a stamp log, one view per job, with the part's start pts so the job asks in part-relative pts:

   ```go
   log := stamp.NewLog(goavpipe.KindOverlay)
   log.OnEvent(func(ev goavpipe.Timed) { /* persist */ })
   log.SetAssetFetcher(func(hash string) { /* fetch, then log.PutAsset(hash, png) */ })
   params.Events = log.Source(partStartPts)
   ```

2. Feed the log. From the input's own clock feed, hand every `AV_IN_STAT_DATA_ANC` to a stamper and end the log
   when the input closes:

   ```go
   stamper := stamp.NewANC(log, layout.Timescale, stamp.DefaultVenueMapping())
   // in InputHandler.Stat: stamper.Feed(args.(*goavpipe.AncPacket))   // must not block
   // on close:            log.End(goavpipe.KindOverlay)
   ```

   From an API, append snapshots and move the seal yourself: `log.Append(ev)`, `log.Seal(kind, writtenPts)`.
   The seal is your promise that nothing will be appended at or below it; the log does not check, it only
   answers *not yet* for a frame past it. For a rendered board the log needs an asset fetcher, a no-op when the
   renderer pushes pictures in with `PutAsset`, or an unknown hash is an error rather than a wait.

3. Validate what you store. `layout.ValidateEvent(&ev)` refuses a value that does not fit a slot, so a bad event
   is caught where it is made rather than failing a playout job.

For a rendered board, `stamp.NewRenderMapping(boardSlot, clockSlot, shotSlot, render)`: the board is one image
slot named by the hash of the board state, `render` is called once per new state, and the renderer puts the
picture in the log with `PutAsset`; the job waits for it. The clocks are drawn by avpipe in holes the picture
leaves, so a ticking clock never costs a render.

## elvxc

| Flag | Does |
|---|---|
| `transcode --overlay spec.json` | Layout, images and fonts from a spec file, with optional events (`t` seconds or `pts`) served by a static source |
| `--overlay-events fifo` | JSON-line patches merged into the state and stamped on the next frame by an in-process stamper; a `{"png": base64}` value becomes an asset by hash |
| `--overlay-anc` | The overlay driven by the input's ST 2038 venue clock feed; slots named `home`, `away`, `score`, `period`, `clock`, `shot` |
| `--overlay-render req.fifo,res.fifo` | With `--overlay-anc`: the board as one picture from an external renderer, `{"hash","state"}` lines out, `{"hash","png"}` back |
| `--overlay-log file` | With `--overlay-events`: every stamped event as a JSON line |
| `--anc-stream-id pid` | The ST 2038 stream's PID when the PMT does not mark it (also on `anc`) |
| `anc -f stream.ts --clock [--events-out f.json]` | Dump the ancillary data and the decoded clock feed; write the events it makes |

## Tests to copy

- `avpipe_overlay_test.go`: a `StaticSource` run against a `sealedSource` that seals a window at a time, so
  frames wait on *not yet*; the two outputs must be byte-identical, and the same oracle covers images by hash.
  The timeout, hold-once and cancel paths assert error codes, ask counts and elapsed time, and a frame extracted
  before the first event shows an image slot's default.
- `avpipe_overlay_anc_test.go`: a TS generated by `broadcastproto/anctest` with a synthetic venue feed in its ST
  2038 stream, stamped live from the stat, byte-identical to the same events up front, for text slots and for a
  rendered board with a fake renderer.
- `goavpipe/stamp/*_test.go`: the log's seal and views, and the venue stamper's clock rules with synthetic
  messages.

Pin the fast encode params (`setFastEncodeParams`) in byte-identity tests; a 720p libx264 encode is not
reproducible across runs.

## Running

Build as usual; the compositor needs no extra libraries. The test font is a system TrueType file (the tests skip
without one). The ST 2038 generator is cgo over libavformat. A live run reads `udp://` with `--seekable=false`.
