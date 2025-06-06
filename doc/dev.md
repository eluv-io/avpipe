# DEVELOPER NOTES



## HOW TO

### Ingest source file into mez parts


Example: audio 2 mono to 1 stereo
```
./bin/exc -f sample.mxf -xc-type audio-join   -format fmp4-segment -seg-duration 30.080 -audio-index 1,2 -channel-layout 3 -audio-bitrate 128000
```

### Transcode Mez Parts into ABR segments

#### Transcode a "mez part" into ABR segments

```
./bin/exc -f hqp_3VQ52.mp4 -format dash -xc-type video -video-seg-duration-ts 120120
```

- output files: `./O/O1/`


#### Transcode a "mez part" into encrypted ABR segments


```
./bin/exc -f hqp_3VQ52.mp4 -format dash -xc-type video -video-seg-duration-ts 120120 -crypt-scheme cenc -crypt-key 13c396f04c947a0b0c9794f7f114a614 -crypt-kid 351a81dbaf704ab43ce7f3b4fbc603b3 -crypt-iv 9c33afa0bff3c27f671fa8e91f31f2c9
```

- output files: `./O/O1/`

### Live Stream Ingest

#### MPEGTS (aka UDP)

```
ffmpeg -re -i test.mp4 -map 0 -c copy -f mpegts udp://127.0.0.1:9000

./bin/exc -f udp://127.0.0.1:9000 -xc-type all -format fmp4-segment -seg-duration 30
```

A more complex example - as called by the content fabric (source 50 fps):

```
./bin/exc -f udp://127.0.0.1:9000 -xc-type all -format fmp4-segment -video-bitrate 9500000 -audio-bitrate 192000 -sample-rate 48000  -video-seg-duration-ts 2700000  -audio-seg-duration-ts 1428480   -force-keyint 100 -enc-height 1080 -enc-width 1920 -sync-audio-to-stream-id 512   -audio-index 1,2  -copy-mpegts 1
```

#### SRT

```
ffmpeg -re -i test.mp4 -map 0 -c copy -f mpegts srt://127.0.0.1:9000

./bin/exc -f srt://127.0.0.1:9000?mode=listener -xc-type all -format fmp4-segment -seg-duration 30
```

#### RTP

```
ffmpeg -re -i test.mp4 -map 0 -c copy -f rtp_mpegts rtp://127.0.0.1:9000

./bin/exc -f rtp://127.0.0.1:9000 -xc-type all -format fmp4-segment -seg-duration 30

```

#### RTMP

```
ffmpeg -re -i test.mp4 -map 0 -c copy -f flv rtmp://127.0.0.1:9000

./bin/exc -f rtmp://127.0.0.1:9000 -xc-type all -format fmp4-segment -seg-duration 30

```


## Special Use Cases

### Deinterlacing

Deinterlacing using the bwdiff "field" filter creates two frames for each input frame and requires adjusting the framerate.
When doubling the framerate the timebase (timescale) may not accommodate the new frame duration and it needs to be adjusted as well.

Deinterlace a source file (25fps) using CRF 16 and max_rate 20 Mbps.  Note video timebase changes to 50 to accommodate new frame duration - this will be adjusted by ffmpeg to 12,800.  The resulting frame duration is 265  (12800/50).

```
  ./bin/exc -f TestFile.mxf -format fmp4-segment -xc-type video  -crf 16 -seg-duration 30.000 -force-keyint 100 -enc-height 1080 -enc-width 1920 -deinterlace 1  -video-time-base 50 -video-frame-duration-ts 256 -rc-max-rate 200000000 -rc-buffer-size 40000000
```

