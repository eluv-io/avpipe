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

When doubling the framerate the timebase (timescale) may not accommodate the new frame duration and it needs to be adjusted as well.

For example if the input frame rate is 25 and timebase is 1/25, the input frame duration is 1.  When deinterlacing using the bwdif "field" filter
we need to change the output timebase to 1/50 and the frame rate becomes 50 fps.

Note that ffmpeg will force the timebase denominator to be greater than 10,000 and will double our "1/50" timebase until it becomes 1/12800.  The new frame duration,
for 50 fps, will be 256.
