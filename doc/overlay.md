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
| Stamper core | `goavpipe/stamp` | `Log`: the stamped events of a stream with a seal per kind, per-job views with a pts offset, assets by hash, an event hook for persistence. `ANC`: the ancillary data of the `AV_IN_STAT_DATA_ANC` stat stamped into events. A `Feed` decodes it into keys (`VenueFeed` for the ST 2038 venue clock feed, `broadcastproto/anc/venueclock`), and `Bindings` turn the keys into slot values as the layout's slots say (`OverlaySlot.From`) |
| ANC stat | `AV_IN_STAT_DATA_ANC`, `goavpipe.AncPacket` | Every ST 2038 PES packet of the input, delivered off the demux thread through a bounded queue; on a live input a consumer that falls behind loses packets and `Dropped` says how many |
| Frame-written stat | `AV_OUT_STAT_FRAME_WRITTEN`, `avpipe.EncodingFrameStats` | Every encoded frame as it is handed to the muxer: the counts, and `LastPts` with the output stream's time base. The written head is the maximum seen; it is the seal for API-fed events and a safe seal for in-band ones when the ancillary stream pauses |

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
   feed := stamp.NewVenueFeed()
   bindings, err := stamp.NewBindings(feed, layout, nil) // a binding that does not fit is refused here
   stamper := stamp.NewANC(log, layout.Timescale, feed, bindings)
   // in InputHandler.Stat: stamper.Feed(args.(*goavpipe.AncPacket))   // must not block
   // on close:            log.End(goavpipe.KindOverlay)
   ```

   From an API, append snapshots and move the seal yourself: `log.Append(ev)`, `log.Seal(kind, writtenPts)`,
   with `writtenPts` the frame-written stat's `LastPts` converted to the overlay timescale (`Seal` only moves
   forward, so the raw per-frame value is fine). The seal is your promise that nothing will be appended at or
   below it; the log does not check, it only answers *not yet* for a frame past it. For a rendered board the log needs an asset fetcher, a no-op when the
   renderer pushes pictures in with `PutAsset`, or an unknown hash is an error rather than a wait.

3. Validate what you store. `layout.ValidateEvent(&ev)` refuses a value that does not fit a slot, so a bad event
   is caught where it is made rather than failing a playout job.

## Keys and bindings

A feed publishes what it decodes as named keys: strings, numbers, bools and timers (a clock state with the pts
it was sampled at). What a graphic shows of them is the layout's business. Each slot may carry a binding, `from`:

| Slot | `from` | Shows |
|---|---|---|
| text | `{"key": "home"}` | The key's value; hidden while the key is unset |
| text | `{"text": "{score_home} - {score_away}"}` | The string with each `{key}` replaced; hidden while every key in it is unset |
| clock | `{"timer": "clock"}` | The timer, carried to the event's pts and drawn in the slot's `format` |
| image | `{"key": "poss", "images": {"home": "arrow_left", "away": "arrow_right"}}` | The library image for the key's value; hidden for a value without an entry. Without `images` the value is the image name |
| image | `{"renderer": "template", "keys": ["home", "away", "poss"]}` | One picture of those keys, named by the hash of their values |

A text or clock slot without a binding takes the feed's default for its name, or else the key of its own name.
An image slot without one is a fixed part of the graphic. `NewBindings` checks every binding against the slot's
type and the keys the feed declares (`Feed.KeyKinds`), so a misspelt key or a clock bound to a text slot is an
error when the stamper is set up, not a failed playout job. A key no slot is bound to makes no event.

The venue feed's keys (`VenueFeed`): `home`, `away`, `home_name`, `away_name`, `score_home`, `score_away`,
`period`, `period_type`, `period_number`, `poss`, `fouls_home`, `fouls_away`, `bonus_home`, `bonus_away`,
`to_home`, `to_away`, `to_short_home`, `to_short_away`, `timeout`, `horn`, `shot_horn`, `shot_expired`,
`period_end`, `time_of_day`, and the timers `clock`, `shot`, `break_clock` and `timeout_clock`. Its defaults are
the six slots `home`, `away`, `score`, `period`, `clock` and `shot`. `VenueMapping` is the older short form that
names a slot per value; `VenueMapping.Bindings()` goes in `NewBindings` as `named`.

For a template binding, `Bindings.Render` is called once per distinct picture with its hash and the keys it
shows, and the renderer puts the picture in the log with `PutAsset`; the job waits for it. The clocks are drawn
by avpipe in holes the picture leaves, so a ticking clock never costs a render. `stamp.WithBoard(layout)` is the
short form of a rendered board: it binds the layout's board slot to a template of `stamp.BoardKeys`.
`stamp.RenderClient` talks to an external renderer over two FIFOs.

Another wire format is another `Feed`: it declares its keys and publishes them from `Decode`. Whether a clock
runs is the feed's to say, since it depends on how the format behaves; the stamper applies the tolerance to every
timer.

## elvxc

| Flag | Does |
|---|---|
| `transcode --overlay spec.json` | Layout, images and fonts from a spec file, with optional events (`t` seconds or `pts`) served by a static source. An image is a PNG path relative to the spec, or the PNG itself in base64 as a layout in the fabric's recording config carries it; a font is a path, relative to the spec or absolute, or a fontconfig pattern such as `Arial:bold` |
| `--overlay-events fifo` | JSON-line patches merged into the state and stamped on the next frame by an in-process stamper; a `{"png": base64}` value becomes an asset by hash |
| `--overlay-anc` | The overlay driven by the input's ST 2038 venue clock feed: each slot of the spec by its `from`, or by name (`home`, `away`, `score`, `period`, `clock`, `shot`, or any key) |
| `--overlay-render req.fifo,res.fifo` | With `--overlay-anc`: the external renderer of the spec's template bindings, `{"hash","state"}` lines out, `{"hash","png"}` back; a spec that binds no template gets the board's on its board slot |
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
without one). A slot's `font` is a font file or, when it names no file, a fontconfig pattern (`Arial:bold`,
`Liberation Sans:bold`), which needs an ffmpeg built with fontconfig. fontconfig always answers with some font:
a pattern that matches nothing, and a file that is missing, are drawn in its default font without an error. The ST 2038 generator is cgo over libavformat. A live run reads `udp://` with `--seekable=false`.
