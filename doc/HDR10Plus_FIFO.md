HDR10+ FIFO Producer (quick guide)

Overview

This document shows a tiny FIFO-based producer that streams per-frame HDR10+ JSON into the avpipe runtime. The `exc` process can consume the FIFO via `-hdr10plus-file fifo:/path` and will call `avpipe_set_hdr10plus(pts, json)` for each line.

Line format

Each line must be:

    <PTS><space><JSON>\n

- `<PTS>`: integer timestamp in the same units the encoder expects (timebase units).
- `<space>`: single space char separating PTS and JSON.
- `<JSON>`: HDR10+ JSON payload (may contain commas and spaces).

Note: the reader splits on the first space only, so the remainder of the line is treated as the JSON payload.

Quick end-to-end test

1. Create a FIFO:

```sh
mkfifo /tmp/hdr10p.fifo
```

2. Start `exc` and point it at the FIFO (must be started BEFORE the producer; otherwise the producer will block when opening the FIFO):

```sh
./exc/bin/exc \
  -f media/bbb_sunflower_2160p_30fps_normal_2min.ts \
  -hdr10plus-file fifo:/tmp/hdr10p.fifo \
  -hdr10plus-tolerance 2 \
  -hdr10plus-ttl 30 \
  -hdr10plus-capacity 10000 \
  -format fmp4-segment \
  -seg-duration 2 \
  -video-seg-duration-ts 180000 \
  -audio-seg-duration-ts 192000 \
  -e libx265 \
  -video-bitrate 5000000 \
  -audio-bitrate 128000 \
  -video-time-base 90000
```

3. In a separate terminal, build and run the tiny Go producer (provided in `tools/hdr10pp_fifo_producer.go`):

```sh
# from repo root
go build -o tools/hdr10pp_fifo_producer tools/hdr10pp_fifo_producer.go
./tools/hdr10pp_fifo_producer -fifo /tmp/hdr10p.fifo -count 50 -start 1000 -step 3000 -delay_ms 50
```

4. Observe that `exc`/encoder receives metadata by PTS. The encoder calls `avpipe_get_hdr10plus(pts)` to fetch and consume the JSON. Output segments will be written to `./O/` directory.

Quick one-liner producer (shell)

You can send a single entry quickly with:

```sh
printf "12345 {\"PayloadVersion\":1,\"Notes\":\"test\"}\n" > /tmp/hdr10p.fifo
```

Notes & tips

- Ensure PTS units match the encoder/avpipe timebase. If you send mismatched units, the configured `-hdr10plus-tolerance` may be required to match entries.
- Entries are consumed once (one-shot) when the encoder reads them; the metadata is removed from the store after retrieval (destructive read).
- Tune `-hdr10plus-ttl` and `-hdr10plus-capacity` to bound memory usage for large or bursty metadata.

Go API alternative

If you run inside the same process as avpipe, you can call the Go wrapper directly:

```go
import "github.com/eluv-io/avpipe/goavpipe"

_ = goavpipe.SetHdr10Plus(pts, []byte(jsonStr))
```

This is useful for in-process producers (no FIFO needed).

Verifying HDR10+ in output segments

After encoding with HDR10+ metadata, verify it's present in the output:

```sh
# Check for dynamic HDR side data in frames
ffprobe -v error -show_frames -select_streams v:0 -of json O/vfsegment0-00001.mp4 | jq '.frames[0].side_data_list'

# Look for SEI HDR10+ messages in trace
ffmpeg -v trace -i O/vfsegment0-00001.mp4 -c copy -f null - 2>&1 | grep -i "dynamic\|hdr10\|sei_type"

# Dump stream-level metadata
ffprobe -show_streams O/vfsegment0-00001.mp4 | grep -i "color\|transfer\|primaries"
```

HDR10+ metadata appears as:
- `AV_FRAME_DATA_DYNAMIC_HDR_PLUS` side data in frame dumps
- SEI type 4 (user data registered) messages with ST 2094-40 payload
- May require proper color transfer (smpte2084/PQ) and primaries set on input for full HDR signaling
