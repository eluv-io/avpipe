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

## Special Use Cases

### Deinterlacing

```
./bin/exc -f ./test.mp4 -format fmp4-segment -seg-duration 30 -video-time-base 100 -video-frame-duration-ts 256 -force-keyint 100

./elvxc/elvxc transcode -f ./test.mxf --video-time-base 100 --video-frame-duration-ts 256 --xc-type video --deinterlace 1 --force-keyint 100 --format fmp4-segment
```
