
## OVERVIEW

This doc describes both conceptual and speific test cases that need to be implemented as part of the unit test system.

## TEST CASES

## SPECIFIC TESTS - ABR SEGMENTS

### Partial segments

##### Should make a 1-frame audio segment

Currently makes 2 frames (the extra frame is too early when bypass=0 though it is correctly skipped)

```
./bin/exc -f media/MEZ_PARTS/hqp_RkEebWhoDuq2BZuWCp1pWMxbp1V7hMQzjGqzFrmWSCB2oitNr -format dash -bypass 0 -xc-type audio -seekable 0 -start-time-ts 1047552 -start-pts 308551680 -duration-ts 768 -start-segment 3252 -audio-bitrate 128000 -sample-rate 48000 -audio-seg-duration-ts 95232 -start-frag-index 302344
```

##### Should make a 1-frame audio segment (bypass)

```
./bin/exc -f media/MEZ_PARTS/hqp_RkEebWhoDuq2BZuWCp1pWMxbp1V7hMQzjGqzFrmWSCB2oitNr -format dash -bypass 1 -xc-type audio -seekable 0 -start-time-ts 1047552 -start-pts 308551680 -duration-ts 768 -start-segment 3252 -audio-bitrate 128000 -sample-rate 48000 -audio-seg-duration-ts 95232 -start-frag-index 302344
```
