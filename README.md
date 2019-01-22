# avpipe

## Build

### Prerequisites

- a workspace containing 
  - elv-toolchain
  - elv-crypto
  - content-fabric (for utilities such as eluvio/log and eluvio/errors) (called `YOUR-CONTENT-FABRIC`)

### Clone avpipe

Make a "Go" top level directory - `$GODEV`

Make directory `$GODEV/src/github.com/qluvio`

`git clone` inside directory `$GODEV/src/github.com/qluvio`

### Set environemnt

Call init scripts to set up the necessary environment variables

Note `init-env.sh` takes as arugment the top level workspace directory that contains `content-fabric`

```bash
source init-env.sh <YOUR-CONTENT-FABRIC>
```

### Build C library


Then build:

```bash
  make install
```

```

Build and test Go code

```bash
  go install ./...
  go test ./...
```

Installs bineries under $GODEV/bin

## Design

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
