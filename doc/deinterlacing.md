# Deinterlacing with elvxc

The primary use case for deinterlacing is VOD and live streams transcoding with 
"2 fields per frame" (effectively doubling the frame rate).

Use `--deinterlace 1` to select `bwdif` in `send_field` mode. Each interlaced
input frame produces two  output frames: 25 fps becomes 50 fps, and
30000/1001 fps (29.97) becomes 60000/1001 fps (59.94).

- `--force-keyint` counts output frames
- timestamp durations use the output timescale
- `--video-time-base` specifies the timescale denominator, so `60000` means a timebase of `1/60000`. Use an explicit timescale of at least 10000 to avoid the ffmpeg muxer implicit timescale adjustment.

| Setting | 25i file → 50p | 29.97i live → 59.94p | 25i live → 50p (2.56sec seg) |
| --- | ---: | ---: | ---: |
| `--deinterlace` | 1 | 1 | 1 |
| `--video-time-base` | 12800 | 60000 | 12800 |
| `--video-frame-duration-ts` | 256 | 1001 | 256 |
| `--equal-fduration` | — | — | true |
| `--force-keyint` | 100 | 120 | 128 |
| `--video-seg-duration-ts` | 384000 | 1801800 | 491520 |
| `--sample-rate` | — | — | 48000 |
| `--audio-seg-duration-ts` | — | — | 1843200 |
| `--seg-duration` | — | — | 38.4 |
| GOP duration | 2 seconds | 2.002 seconds | 2.56 seconds |
| Mez part duration | 30 seconds | 30.03 seconds | 38.4 seconds (15 × 2.56) |

## 25 fps interlaced file ingest

```sh
./bin/elvxc transcode \
  --filename input-25i.mxf \
  --format fmp4-segment --xc-type video \
  --encoder libx264 --crf 16 \
  --deinterlace 1 \
  --video-time-base 12800 --video-frame-duration-ts 256 \
  --force-keyint 100 --video-seg-duration-ts 384000
```

## 29.97 fps interlaced live MPEG-TS ingest

```sh
./bin/elvxc transcode \
  --filename 'udp://0.0.0.0:5000' --seekable=false \
  --format fmp4-segment --xc-type video \
  --encoder libx264 --crf 16 \
  --deinterlace 1 \
  --video-time-base 60000 --video-frame-duration-ts 1001 \
  --force-keyint 120 --video-seg-duration-ts 1801800
```

## 25 fps interlaced live MPEG-TS ingest (15 × 2.56-second ABR segments per mez part)

This Live TV example produces 50p video and AAC audio with a 38.4-second
mezzanine part target for both (2.56-second ABR segments).
Each ABR interval contains 128 video frames and 120 AAC frames at 48 kHz

```sh
./bin/elvxc transcode \
  --filename 'udp://127.0.0.1:5000' --seekable=false \
  --format fmp4-segment --xc-type all \
  --encoder libx264 --preset medium --profile high --video-bitrate 9500000 \
  --enc-width 1920 --enc-height 1080 \
  --audio-encoder aac --audio-index 1 --audio-bitrate 192000 --sample-rate 48000 \
  --sync-audio-to-stream-id 256 \
  --deinterlace 1 --equal-fduration \
  --video-time-base 12800 --video-frame-duration-ts 256 --force-keyint 128 \
  --seg-duration 38.4 --video-seg-duration-ts 491520 --audio-seg-duration-ts 1843200 \
  --copy-packaging ats_ts
```

## Regression tests

Generated-source tests

```sh
go test ./xc -run '^TestDeinterlaceMez$' -count=1
```

Live capture tests

```sh
go test ./xc -run '^TestDeinterlaceLiveCaptures$' -count=1
```

Uses 5 min PCAP prefixes from two real interlaced sources.

| Case | Input → output | Video timescale / frame ticks | Key interval | `seg_duration` | Video segment ticks | Audio segment ticks (48 kHz) |
| --- | --- | --- | ---: | ---: | ---: | ---: |
| Live sports | 29.97i → 59.94p | 60000 / 1001 | 120 | 30.03 | 1801800 (30.03 s) | 1428480 (29.76 s) |
| Live TV | 25i → 50p | 12800 / 256 | 128 | 2.56 | 32768 (2.56 s) | 122880 (2.56 s) |

