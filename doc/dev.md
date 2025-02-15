# DEVELOPER NOTES



## HOW TO

### Transcode a "content part" into ABR segments

```
./bin/exc -f hqp_3VQ52.mp4 -format dash -xc-type video -video-seg-duration-ts 120120
```

- output files: `./O/O1/`


### Transcode a "content part" into encrypted ABR segments


```
./bin/exc -f hqp_3VQ52.mp4 -format dash -xc-type video -video-seg-duration-ts 120120 -crypt-scheme cenc -crypt-key 13c396f04c947a0b0c9794f7f114a614 -crypt-kid 351a81dbaf704ab43ce7f3b4fbc603b3 -crypt-iv 9c33afa0bff3c27f671fa8e91f31f2c9
```

- output files: `./O/O1/`


## Special Use Cases

### Deinterlacing

Deinterlacing using the bwdiff "field" filter creates two frames for each input frame and requires adjusting the framerate.
When doubling the framerate the timebase (timescale) may not accommodate the new frame duration and it needs to be adjusted as we..

Deinterlace a source file (25fps) using CRF

```
  ./bin/exc -f TestFile.mxf -format fmp4-segment -xc-type video  -crf 16 -seg-duration 30.000 -force-keyint 100 -enc-height 1080 -enc-width 1920 -deinterlace 1  -video-time-base 50 -video-frame-duration-ts 256 -rc-max-rate 100000000 -rc-buffer-size 200000000
  ```


```
./bin/exc -f ./test.mp4 -format fmp4-segment -seg-duration 30 -video-time-base 100 -video-frame-duration-ts 256 -force-keyint 100

./elvxc/elvxc transcode -f ./test.mxf --video-time-base 100 --video-frame-duration-ts 256 --xc-type video --deinterlace 1 --force-keyint 100 --format fmp4-segment
```
