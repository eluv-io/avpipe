# AVPIPE TRANSCODING


## Job Types

The avpipe library is designed to accommodate specific transcoding operations.

### Mez creation - VOD

The input file can be any format and any size - this is the source media for the mezzanine.

The operation creates 30 second (approx) content parts, fragmented MP4, encoded at the desired resolution and bitrate, and conditioned with GOP and keyframe interval of approx 2 sec.


### Mez creation - Live

The input is a live stream RTMP, MPEGTS, SRT or HLS.
The operation creates 30 second (approx) content parts, fragmented MP4, encoded at the desired resolution and bitrate, and conditioned with GOP and keyframe interval of approx 2 sec.

### ABR segments

The input is a conditioned mez content part, as created by the mez creation operation

The output is a set of "CMAF" segments (2 sec approx) and the corresponding 'init' segment

### Mux-only File Maker

> TODO

### File Maker

> TODO

### Thumbnail Extraction

The input is a conditioned mez content part, as created by the mez creation operation.

The output is a set of individual frames.

## Common Use Cases

> TODO

## Special Use Cases

### Deinterlacing

Deinterlacing using the bwdif "field" filter creates two frames for each input frame and requires adjusting the framerate.

When doubling the framerate the timebase (timescale) may not accommodate the new frame duration and it needs to be set to a value that works.

- specify `video_time_base` when the source time base doesn't accommodate the double frame rate (also use a value over 10,000 to avoid the automatic ffmpeg adjustment)
- no need to specify `video_frame_duration_ts` (this was necessary in the past) - only specify it if you need all frames to be force-stamped with consecutive timestamps

| Input | Output | `video_time_base` | `video_frame_duration_ts` | `force_keyint` | `video_seg_duration_ts` |
| --- | --- | --- | --- | --- | --- |
| 25 interlaced fps | 50 progressive fps | 12800 | 256 | 100 | 384000 (30 seconds) |
| 30000/1001 interlaced fps | 60000/1001 progressive fps | 60000 | 1001 | 120 | 1801800 (30.03 seconds) |

