# avpipe

# Build

## Prerequisites

A workspace containing
 - elv-toolchain
 - elv-crypto
 - content-fabric (for utilities such as eluvio/log and eluvio/errors)

## Clone avpipe

1. Make a "Go" top level directory - `$GODEV`
1. Make directory `$GODEV/src/github.com/qluvio`
1. `git clone` inside directory `$GODEV/src/github.com/qluvio`

## Set Environment

```bash
source init-env.sh <content-fabric-path>
```

## Build and Test

```bash
make
make install
go install ./...
go test ./...
```
Binaries are installed under $GODEV/bin



## Full Tests

1. Download media test files

  The test files are stored at: https://console.cloud.google.com/storage/browser/qluvio-test-assets

  ```
  cd ./media
  gsutil -m cp 'gs://qluvio-test-assets/*' .
  ```

2. Run the tests

  ```
  go test ./...
  ```

# Design

### Parameters

- Ouput
  - type: `DASH` or `DASH+HLS` (which will generate both DASH and HLS manifests)
    (test program just needs a directory)

- Encoding
  - video
    - bitrate
    - crf (if present bitrate is not accpepted)
	- fps
    - codec
    - widht x height
  - audio
    - bitrate
    - sample_rate
	- codec

- Segmenting
  - segment_duration - rational number; must be multiple of frame duration
    - or segment_duration_ts (or should we require it be in number of frames?)
  - start_segment
  - start_time  - rational; multiple of frame duration
    - or 'start_time_ts'
  - end_time    - rational; multiple of frame duration
    - or 'end_time_ts'

- Source info (used for checking parameters - can be extracted and then checked)
  - time_base - rational (example 1/ 30000)
  - frame_rate - rational (example 30000 / 1001)

Parameter verification

- segment_duration is a multiple of frame duration
- start_time and end_time are multiple of frame duration


### Counters

- bytes read from source when completing
  - avformat_open_input
  - avformat_find_stream_info
  - decoding first packet/frame
  - producing the first encoded frame
  - make the first write to the first outout segment
  - finish the first output segment

- time spent in reading source data (min/max and total time or avaerage)
- time to generating each segment

- exceptions
  - number of segments starting without a keyframe




## Design map

### Input (reading from source)

INIT

libavpipe avpipe_init
  - allocaes txctx
  - in_handlers - avpipe_reader

SETUP
  - avformat_open_input                   --> read()
  - avformat_find_stream_info             --> read()

WORK LOOP

  - avpipe_tx()
    - write header
	- loop
	  - read_frame                        --> read()
	  - decode_packet
	    - avcodec_send_packet     (to decoder)
		- while - avcodec_receive_frame   (from decoder)

        - av_buffersrc_add_frame_flags()
		- while - av_buffersink_get_frame()

		- encode_frame
          - avcodec_send_frame
		  - avcodec_receive_packet

		  - av_write_frame               --> open() write() close()


## Implemenation TODO list

- SEEK should fail because it doesn't work with a stream reader
- how to return errors!
- prepare_decoder() -- see if we can remove bogus.mp4 filename


## Setting up live

- Avpipe can handle HLS, UDP TS, and RTMP live streams. For each case it is needed to set parameters for live stream properly.
- If the parameters are set correctly, then avpipe recorder would read the live data and generate live audio/video mezzanine files.
- In order to have a good quality output, the audio and video live has to be synced. Avpipe currently, uses 2 methods to sync the audio and video.
  1) Syncing with 10 sec skip of audio and video. This will be enabled if sync_audio_to_stream_id is set to -1 in audio live params.
  2) Syncing the selected audio with one of the elementary video streams, specified by sync_audio_to_stream_id, based the first key frame in the video stream. In this case, sync_audio_to_stream_id would be set to the stream id of the video elementary stream in audio params.
- The default value of sync_audio_to_stream_id is 0, which means avpipe would not skip for 10 sec and will sync the audio with stream id 0.
- For HLS and RTMP, there is no need to be worried about syncing audio and video streams. Both audio and video would be synced automatically by avpipe.


### HLS live sample params
```
const hlsStream = {
  audioTxParams: {
    "audio_bitrate": 128000, 	// required
    "audio_index": 11, 		// required
    "dcodec2": "aac", 		// required
    "sample_rate": 48000, 	// Hz, required
    // 24s @ 48000 Hz @ 1024 samples per frame (1443840 for 30.08s)
    "audio_seg_duration_ts": 1152000, // part size, required
  },
  ingestType: "hls", 			// default: hls
  maxDurationSec: 600, 			// 10m, default: 2h
  originUrl: "http://yourhlsplaylist", 	// required for hls
  sourceTimescale: 90000, 		// required
  videoTxParams: {
    "enc_height": 720, 			// required
    "enc_width": 1280, 			// required
    "force_keyint": 40, 		// frames, required
    "video_seg_duration_ts": 2160000, 	// 24s @ 25 fps, part size, required
    "video_bitrate": 8000000, 		// 8 Mbps, required
  },
}

```
### UDP live sample params
- In this example some paramters are also set for watermarking the time on each frame.
```
const udpTsStream = {
  audioTxParams: {
    "audio_bitrate": 128000,    // required
    "audio_index": 0,           // required
    "dcodec2": "ac3",           // required
    "ecodec2": "aac",           // required
    "sample_rate": 48000,       // Hz, required
    "seg_duration": "30.03",
    "format": "fmp4-segment",
    "audio_seg_duration_ts": 1443840, // 30s @ 48000 Hz @ 1024 samples per frame; part size, required
  },
  ingestType: "udp",
  maxDurationSec: 600,
  sourceTimescale: 90000,
  udpPort: 22001, 		// required for udp
  xcType: "all",                // all, audio, video
  simpleWatermark: {
    "font_color": "white@0.5",
    "font_relative_height": 0.05000000074505806,
    "shadow": true,
    "shadow_color": "black@0.5",
    "template": "%{pts\:gmtime\:1602968400\:%d-%m-%Y %T}",
    "x": "(w-tw)/2",
    "y": "h-(2*lh)"
  },
  videoTxParams: {
    "enc_height": 720,
    "enc_width": 1280,
    "force_keyint": 120,
    "format": "fmp4-segment",
    "seg_duration": "30",
    "video_seg_duration_ts": 2702700, 	// 30s @ 60000/1001 fps
    "video_bitrate": 20000000, 		// 20 Mbps
  },
}
```
### RTMP live sample params
```
const rtmpStream = {
  audioTxParams: {
    "audio_bitrate": 128000,    // required
    "audio_index": 1,           // required
    "dcodec2": "ac3",           // required
    "ecodec2": "aac",           // required
    "sample_rate": 48000,       // Hz, required
    "seg_duration": "30.03",
    "audio_seg_duration_ts": 1441440, //1443840 30s @ 48000 Hz @ 1024 samples per frame; part size, required
  },
  ingestType: "rtmp",
  maxDurationSec: 600,
  sourceTimescale: 16000,
  xcType: "all",                // all, audio, video
  rtmpURL: "rtmp://localhost:5000/test001",
  listen: true,
  videoTxParams: {
    "enc_height": 720,
    "enc_width": 1280,
    "force_keyint": 48,
    "format": "fmp4-segment",
    "seg_duration": "30",
    "video_seg_duration_ts": 480480,    // 30*16000
    "video_bitrate": 20000000,          // 20 Mbps
  },
}
```
