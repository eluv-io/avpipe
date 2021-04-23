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


## Setting up live

- Avpipe can handle HLS, UDP TS, and RTMP live streams. For each case it is needed to set parameters for live stream properly.
- If the parameters are set correctly, then avpipe recorder would read the live data and generate live audio/video mezzanine files.
- Using tx-all transcoding feature, which was added recentely, avpipe can transcode both audio and video of a live stream and produce mezzanine files.
- In order to have a good quality output, the audio and video live has to be synced.
- If input has multiple audios, avpipe can sync the selected audio with one of the elementary video streams, specified by sync_audio_to_stream_id, based the first key frame in the video stream. In this case, sync_audio_to_stream_id would be set to the stream id of the video elementary stream.


## Transcoding with preset

- Avpipe has the capability to apply preset parameter when encoding using H264 encoder.
- The experiments shows that using preset `faster` instead of `medium` would generate almost the same size output/bandwidth while keeping the picture quality high.
- The other advantage of using preset `faster` instead of `medium` is that it would consume less CPU and encode faster.
- To compare the following command is used to generate the mezzanines for `creed_5_min.mov`:

```
./bin/etx -f ../media/MGM/creed_5_min.mov -tx-type all -force-keyint 48 -seg-duration 30.03 -seekable 1 -format fmp4-segment -> preset medium
./bin/etx -f ../media/MGM/creed_5_min.mov -tx-type all -force-keyint 48 -seg-duration 30.03 -seekable 1 -format fmp4-segment -preset faster -> preset faster

                                                         +-----------------------------------------------------------------------------------------------------------+
                                                         |                                           Generated Segments (Bytes)                                      |
+------------+-----------------------+-------------------+-----------------------------------------------------------------------------------------------------------+
|   Preset   |      Input file       |  transcoding time |  Seg 1  |  Seg 2  |  Seg 3   |  Seg 4   |  Seg 5   |  Seg 6   |   Seg 7  |  Seg 8   |  Seg 9  |  Seg 10   |
+------------+-----------------------+-------------------+-----------------------------------------------------------------------------------------------------------+
|    Medium  |     creed_5_min.mov   |      3m56.218s    | 9832287 | 5068374 | 15237931 | 14886633 | 10418712 | 15105825 | 14009736 | 14090010 | 13788974 | 13206589 |
+------------+-----------------------+-------------------+-----------------------------------------------------------------------------------------------------------+
|    Faster  |     creed_5_min.mov   |      3m3.294s     | 10004519| 4570883 | 14698976 | 14470983 | 10156648 | 13879914 | 13820847 | 13222401 | 13172066 | 12314343 |
+------------+-----------------------+-------------------+-----------------------------------------------------------------------------------------------------------+

```
- Expermineting with other input files and some RTMP streams showed the same results.
