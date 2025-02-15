# avpipe

## Introduction

The avpipe library is basically a C/Go library on top of FFmpeg with very simple transcoding APIs.
This library helps third party developers to develop server programs or apps for transcoding audio/video in C or Go.
The avpipe library has the capability to transcode audio/video input files and produce MP4 or M4S (for HLS/DASH playout) segments on the fly.
The source of input files for transcoding can be a file on disk, a network connection, or some objects on the cloud.
Depending on the location of the source file, the input of avpipe library can be set via some handlers (callback functions) in both C or Go.
Similarly the output of avpipe library can be set by some callback functions to send the result to a file on disk, a network connection or some objects on the cloud.

Usually the media segments are either fragmented MP4 or TS segments.
So far, the work on avpipe has been focused on generating fragmented MP4 segments (the TS format might be added later).

## Build

### Prerequisites

The following libraries must be installed:

- libx264
- libx265
- cuda-10.2 or cuda-11 (if you want to compile and build to support cuda/nvidia)

### Clone FFmpeg and avpipe

The following repositories can be checked out in any directory, but for better organization it is recommended to be cloned in the same parent directory like _src_
(_src_ is the workspace containing FFmpeg and avpipe).

- eluv-io/FFmpeg:
  - `git clone git@github.com:eluv-io/FFmpeg.git`
- eluv-io/avpipe:
  - `git clone git@github.com:eluv-io/avpipe.git`

### Build FFmpeg and avpipe

- Build eluv-io/FFmpeg: in FFmpeg directory `<ffmpeg-path>` run
  - `./build.sh`
- Build SRT: checkout SRT code `https://github.com/Haivision/srt` and build SRT
- Set environment variables: in avpipe directory run
  - `source init-env.sh <ffmpeg-path> <srt_path>`
- Build avpipe: in avpipe directory run
  - `make`
- This installs the binaries under `<avpipe-path>/bin`
- Note: avpipe has to be built and linked with eluv-io/FFmpeg to be functional.

### Test avpipe

- Download media test files from https://console.cloud.google.com/storage/browser/eluvio-test-assets into `media` directory inside `<avpipe-path>`
  - `cd avpipe`
  - `mkdir ./media`
  - `cd ./media`
  - `gcloud auth login`
  - `gsutil -m cp 'gs://eluvio-test-assets/*' .`
- Inside `<avpipe-path>` run
  - `go test -timeout 2000s`
- Instead of the above commands, you can run the following scripts:
  - `run_tests.sh` to run avpipe core functionality and transcoding tests.
  - `run_live_tests.sh`: to run avipe live-streaming functionality tests.

### Run `exc` and `elvxc`

`exc` and `elvxc` are standalone variants of avpipe that are generally used for testing. They are both installed when running `make` in the root.

`exc` is a lightweight C-only wrapper of avpipe. A detailed usage doc can be printed out by simply running `exc`. It is primarily usable for single-shot transcoding operations.

`elvxc` is a standalone go binary that invokes the avpipe library to do probing, log analysis, muxing, and transcoding.

## Design

- Avpipe library has been built on top of the different libraries of ffmpeg, the most important ones are libx264, libx265, libavcodec, libavformat, libavfilter and libswresample. But in order to achieve all the features and capabilities some parts of ffmpeg library have been changed. Avpipe library is capable of transcoding or probing an input source (i.e a media file, or an UDP/RTMP stream) and producing output media or probe results. In order to start a transcoding job the transcoding parameters have to be set.
- This section describes some of the capabilities and features of avpipe library and how to set the parameters to utilize those features.

### Parameters

```c
typedef struct xcparams_t {
    char    *url;                       // URL of the input for transcoding
    int     bypass_transcoding;         // if 0 means do transcoding, otherwise bypass transcoding
    char    *format;                    // Output format [Required, Values: dash, hls, mp4, fmp4]
    int64_t start_time_ts;              // Transcode the source starting from this time
    int64_t start_pts;                  // Starting PTS for output
    int64_t duration_ts;                // Transcode time period from start_time_ts (-1 for entire source)
    char    *start_segment_str;         // Specify index of the first segment  TODO: change type to int
    int     video_bitrate;
    int     audio_bitrate;
    int     sample_rate;                // Audio sampling rate
    int     channel_layout;             // Audio channel layout for output
    char    *crf_str;
    char    *preset;                    // Sets encoding speed to compression ratio
    int     rc_max_rate;                // Maximum encoding bit rate, used in conjunction with rc_buffer_size
    int     rc_buffer_size;             // Determines the interval used to limit bit rate [Default: 0]
    int64_t     audio_seg_duration_ts;  // For transcoding and producing audio ABR/mez segments
    int64_t     video_seg_duration_ts;  // For transcoding and producing video ABR/mez segments
    char    *seg_duration;              // In sec units, can be used instead of ts units
    int     seg_duration_fr;
    int     start_fragment_index;
    int     force_keyint;               // Force a key (IDR) frame at this interval
    int     force_equal_fduration;      // Force all frames to have equal frame duration
    char    *ecodec;                    // Video encoder
    char    *ecodec2;                   // Audio encoder when xc_type & xc_audio
    char    *dcodec;                    // Video decoder
    char    *dcodec2;                   // Audio decoder when xc_type & xc_audio
    int     gpu_index;                  // GPU index for transcoding, must be >= 0
    int     enc_height;
    int     enc_width;
    char    *crypt_iv;                  // 16-byte AES IV in hex (Optional, Default: Generated)
    char    *crypt_key;                 // 16-byte AES key in hex (Optional, Default: Generated)
    char    *crypt_kid;                 // 16-byte UUID in hex (Optional, required for CENC)
    char    *crypt_key_url;             // Specify a key URL in the manifest (Optional, Default: key.bin)
    int     skip_decoding;              // If set, then skip the packets until start_time_ts without decoding

    crypt_scheme_t  crypt_scheme;       // Content protection / DRM / encryption (Default: crypt_none)
    xc_type_t       xc_type;            // Default: 0 means transcode 'everything'

    int         seekable;               // Default: 0 means not seekable. A non seekable stream with moov box in
                                            //          the end causes a lot of reads up to moov atom.
    int         listen;                     // Default is 1, listen mode for RTMP
    char        *watermark_text;            // Default: NULL or empty text means no watermark
    char        *watermark_xloc;            // Default 0
    char        *watermark_yloc;            // Default 0
    float       watermark_relative_sz;      // Default 0
    char        *watermark_font_color;      // black
    int         watermark_shadow;           // Default 1, means shadow exist
    char        *overlay_filename;          // Overlay file name
    char        *watermark_overlay;         // Overlay image buffer, default is NULL
    image_type  watermark_overlay_type;     // Overlay image type, default is png
    int         watermark_overlay_len;      // Length of watermark_overlay if there is any
    char        *watermark_shadow_color;    // Watermark shadow color
    char        *watermark_timecode;        // Watermark timecode string (i.e 00\:00\:00\:00)
    float       watermark_timecode_rate;    // Watermark timecode frame rate
    int         audio_index[MAX_AUDIO_MUX]; // Audio index(s) for mez making
    int         n_audio;                    // Number of entries in audio_index
    int         audio_fill_gap;             // Audio only, fills the gap if there is a jump in PTS
    int         sync_audio_to_stream_id;    // mpegts only, default is 0
    int         bitdepth;                   // Can be 8, 10, 12
    char        *max_cll;                   // Maximum Content Light Level (HDR only)
    char        *master_display;            // Master display (HDR only)
    int         stream_id;                  // Stream id to trasncode, should be >= 0
    char        *filter_descriptor;         // Filter descriptor if tx-type == audio-merge
    char        *mux_spec;
    int64_t     extract_image_interval_ts;  // Write frames at this interval. Default: -1
    int64_t     *extract_images_ts;         // Write frames at these timestamps.
    int         extract_images_sz;          // Size of the array extract_images_ts

    int         video_time_base;            // New video encoder time_base (1/video_time_base)
    int         video_frame_duration_ts;    // Frame duration of the output video in time base

    int         debug_frame_level;
    int         connection_timeout;         // Connection timeout in sec for RTMP or MPEGTS protocols
} xcparams_t;

```

- **Determining input:** the url parameter uniquely identifies the input source that will be transcoded. It can be a filename, a network URL that identifies a stream (i.e udp://localhost:22001), or another source that contains the input audio/video for transcoding.

- **Determining output format:** avpipe library can produce different output formats. These formats are DASH/HLS adaptive bitrate (ABR) segments, fragmented MP4 segments, fragmented MP4 (one file), and image files. The format field has to be set to “dash”, “hls”, “fmp4-segment”, or “image2” to specify corresponding output format.
- **Specifying input streams:** this might need setting different params as follows:
  - If xc_type=xc_audio and audio_index is set to audio stream id, then only specified audio stream will be transcoded.
  - If xc_type=xc_video then avpipe library automatically picks the first detected input video stream for transcoding.
  - If xc_type=xc_audio_join then avpipe library creates an audio join filter graph and joins the selected input audio streams to produce a joint audio stream.
  - If xc_type=xc_audio_pan then avpipe library creates an audio pan filter graph to pan multiple channels in one input stream to one output stereo stream.
- **Specifying decoder/encoder:** the ecodec/decodec params are used to set video encoder/decoder. Also ecodec2/decodec2 params are used to set audio encoder/decoder. For video the decoder can be one of "h264", "h264_cuvid", "jpeg2000", "hevc" and encoder can be "libx264", "libx265", "h264_nvenc", "h264_videotoolbox", or "mjpeg". For audio the decoder can be “aac” or “ac3” and the encoder can be "aac", "ac3", "mp2" or "mp3".
- **Transcoding multiple audio:** avpipe library has the capability to transcode one or multiple audio streams at the same time. The `audio_index` array includes the audio index of the streams that will be transcoded. The parameter `n_audio` determines the number of audio indexes in the `audio_index` array.
- **Using GPU:** avpipe library can utilize NVIDIA cards for transcoding. In order to utilize the NVIDIA GPU, the gpu_index must be set (the default is using GPU with index 0). To find the existing GPU indexes on a machine, nvidia-smi command can be used. In addition, the decoder and encoder should be set to "h264_cuvid" or "h264_nvenc" respectively. And finally, in order to pick the correct GPU index the following environment variable must be set “CUDA_DEVICE_ORDER=PCI_BUS_ID” before running the program.
- **Text watermarking:** this can be done with setting watermark_text, watermark_xloc, watermark_yloc, watermark_relative_sz, and watermark_font_color while transcoding a video (xc_type=xc_video), which makes specified watermark text to appear at specified location.
- **Image watermarking:** this can be done with setting watermark_overlay (the buffer containing overlay image), watermark_overlay_len, watermark_xloc, and watermark_yloc while transcoding a video (xc_type=xc_video).
- **Live streaming with UDP/HLS/RTMP:** avpipe library has the capability to transcode an input live stream and generate MP4 or ABR segments. Although the parameter setting would be similar to transcoding any other input file, setting up input/output handlers would be different (this is discussed in sections 6 and 8).
- **Extracting images:** avpipe library can extract images either using a time interval or specific timestamps.
- **HDR support:** avpipe library allows to create HDR output while transcoding with H.265 encoder. To make an HDR content two parameters max_cll and master_display have to be set.
- **Bypass feature:** setting bypass_transcoding to 1, would avoid transcoding and copies the input packets to output. This feature is very useful (saves a lot of CPU and time) when input data matches with output and we can skip transcoding.
- **Muxing audio/video ABR segments and creating fMP4/MP4 files:** this feature allows the creation of fMP4/MP4 files from transcoded audio/video segments. In order to do this a muxing spec has to be made to tell avpipe which ABR segments should be stitched together to produce the final fMP4/MP4. To make this feature working xc_type should be set to xc_mux and the mux_spec param should point to a buffer containing muxing spec. If the format is 'fmp4-segment' the output will be fMP4, otherwise MP4.
- **Transcoding from specific timebase offset:** the parameter start_time_ts can be used to skip some input and transcode from specified TS in start_time_ts. This feature is also very useful to start transcoding from a certain point and not from the beginning of file/stream.
- **Audio join/pan/merge filters:**
  - setting xc_type = xc_audio_join would join 2 or more audio inputs and create a new audio output (for example joining two mono streams and creating one stereo).
  - setting xc_type = xc_audio_pan would pick different audio channels from input and create a new audio stream (for example picking different channels from a 5.1 channel layout and producing a stereo containing two channels).
  - setting xc_type = xc_audio_merge would merge different input audio streams and produce a new multi-channel output stream (for example, merging different input mono streams and create a new 5.1)
- **Setting video timebase:** setting `video_time_base` will set the timebase of generated video to 1/video_time_base (the timebase has to be bigger than 10000).
- **Video frame duration:** the parameter `video_frame_duration_ts` can be used to set the duration of each video frame with the specified timebase for output video. This along with video*time_base can be used to normalize the video frames and their duration. For example, for a stream with 60 fps and `video_frame_duration_ts` equal to 256, the `video_time_base` would be 15360. As another example, for a 59.94 fps, the `video_frame_duration_ts` can be 1001 and `video_time_base` would be 60000. In this case a segment of 1800 frames would be 1801800 timebase long.
- **Debugging with frames:** if the parameter debug_frame_level is on then the logs will also include very low level debug messages to trace reading/writing every piece of data.
- **Connection timeout:** This parameter is useful when recording / transcoding RTMP or MPEGTS streams. If avpipe is listening for an RTMP stream, connection_timeout determines the time in sec to listen for an incoming RTMP stream. If avpipe is listening for incoming UDP MPEGTS packets, connection_timeout determines the time in sec to wait for the first incoming UDP packet (if no packet is received during connection_timeout, then timeout would happen and an error would be generated).

### C/Go interaction architecture

Avpipe library has two main layers (components): avpipe C library and avpipe C/GO library.

#### Avpipe C library

The first layer is the C code, mainly libavpipe directory, that has been built on top of the different libraries of ffmpeg like libx264, libx265, libavcodec, libavformat, libavfilter and libswresample.
This is the main transcoding engine of avpipe and defines low level C transcoding API of avpipe library.
Avpipe uses the callbacks in avio_alloc_context() to read, write, or seek into the media files.

As mentioned before this layer uses the callbacks in avio_alloc_context() to read, write, or seek into the media files. These callbacks are used by FFmpeg libraries to read, and seek into the input file (or write and seek to the output file). Avpipe C library provides avpipe_io_handler_t struct to client applications for transcoding (at this moment exc and Go layer are the users of avpipe_io_handler_t); notice that three of these functions are exactly matched to read, write, seek callback functions of avio_alloc_context(), but three more functions are also added to open, close and stat the input or output:

```c
typedef struct avpipe_io_handler_t {
  avpipe_opener_f avpipe_opener;
  avpipe_closer_f avpipe_closer;
  avpipe_reader_f avpipe_reader;
  avpipe_writer_f avpipe_writer;
  avpipe_seeker_f avpipe_seeker;
  avpipe_stater_f avpipe_stater;
} avpipe_io_handler_t;
```

- The `avpipe_opener()` callback function opens the media file (makes a transcoding session) and initializes an ioctx_t structure.
- The `avpipe_closer()` callback function closes the corresponding resources that were allocated for the media file (releases the resources that were allocated with the opener).
- The `avpipe_reader()` callback function reads the packets/frames of the opened media file for transcoding. This callback function automatically gets called by ffmpeg during transcoding.
- The `avpipe_writer()` callback function writes the transcoded packets/frames to the output media file. This callback function automatically gets called by ffmpeg during transcoding.
- The `avpipe_seeker()` callback function seeks to a specific offset and gets called automatically by ffmpeg during transcoding.
- The `avpipe_stater()` callback function publishes different statistics about transcoding and gets called by the avpipe library itself.

In order to start a transcoding session with avpipe C library, the following APIs are provided:

- `avpipe_init(xctx_t **xctx, avpipe_io_handler_t *in_handlers, avpipe_io_handler_t *out_handlers, txparams_t *p):` this initialises a transcoding context with provided input handlers, output handlers, and transcoding parameters.
- `avpipe_fini(xctx_t **xctx):` This releases all the resources associated with the already initialized transcoding context.
- `avpipe_xc(xctx_t *xctx, int do_instrument):` this starts the transcoding corresponding to the transcoding context that was already initialized by avpipe_init(). If do_instrument is set it will also do some instrumentation while transcoding.
- `avpipe_probe(avpipe_io_handler_t *in_handlers, txparams_t *p, xcprobe_t **xcprobe, int *n_streams):` this function probes an input media which can be accessed by in_handlers callback functions. It is recommended to set the seekable parameter to make searching and finding some meta data faster in the input stream if the input stream is not a live stream. Of course, for a live stream seekable should not be set since it is not possible to seek back and forth in live input data.

#### C/Go layer

The second layer is the C/GO code, mainly avpipe.go, that provides the API for Go programs, and avpipe.c, which glues the Go layer to avpipe C library.
The handlers in avpipe.c are the callback functions that get called by ffmpeg and they call the Go layer callback functions themselves.

Avpipe C/Go layer provides APIs that can be used by Go programs. The implementation of this layer is in two files avpipe.c and avpipe.go. The transcoding APIs exposed to Go client programs are designed and implemented in two categories:

**1) APIs with no handle:** these APIs are very simple to use and are useful when the client application is dealing with short transcodings (i.e transcodings that would take 20-30 secs) and the application doesn’t need to cancel the transcoding

**2) Handle based APIs:** which work based on a handle and corresponding transcoding can be cancelled using the specified handle. These transcoding APIs are useful, for example, when dealing with live streams and the application wants to cancel a live stream recording.

All the APIs in the C/Go library can be categories as the following:

##### No handle based transcoding APIs

- `Xc(params *XcParams):` initializes a transcoding context in avpipe and starts running the corresponding transcoding job.
- `Mux(params *XcParams):` initializes a transcoding context in avpipe and starts running the corresponding muxing job.
- `Probe(params *XcParams):` starts probing the specified input in the url parameter. In order to make probing faster, it is better to set seekable in params to true when probing non-live inputs.

##### Handle based transcoding APIs

- `XcInit(params *XcParams):` initializes a transcoding context in avpipe and returns its corresponding 32bit handle to the client code. This handle can be used to start or cancel the transcoding job.
- `XcRun(handle int32):` starts the transcoding job that corresponds to the obtained handle by `XcInit()`.
- `XcCancel(handle int32):` cancels or stops the transcoding job corresponding to the handle.

##### IO handler APIs

- `InitIOHandler(inputOpener InputOpener, outputOpener OutputOpener):` This is used to set global input/output opener for avpipe transcoding. If there is no specific input or output opener for a URL the global input/output opener will be used.
- `InitUrlIOHandler(url string, inputOpener InputOpener, outputOpener OutputOpener):` This is used to set input/output opener specific to a URL when transcoding. The input or output opener set by this function is only valid for the specified url and will be unset after `Xc()` or `Probe()` is complete.
- `InitMuxIOHandler(inputOpener InputOpener, outputOpener OutputOpener):` Sets the global handler for muxing (similar to InitIOHandler for transcoding).
- `InitUrlMuxIOHandler(url string, inputOpener InputOpener, outputOpener OutputOpener):` This is used to set input/output opener specific to a URL when muxing (similar to InitUrlIOHandler for transcoding).

##### Miscellaneous APIs

- `H264GuessProfile(bitdepth, width, height int):` returns the profile.
- `H264GuessLevel(profile int, bitrate int64, framerate, width, height int):` returns the level.

### Setting up Go IO handlers

As mentioned before the source of transcoding in avpipe library can be a file on a disk, a TCP connection like RTMP, some UDP datagrams like MPEGTS stream, or even an object on the cloud. Similarly the output of transcoding in avpipe can be a file on a disk, some memory cache, or another object on the cloud. This flexibility in avpipe is achieved by two interfaces: InputOpener and OutputOpener interface.

#### InputOpener interface

This interface has only one Open() method that must be implemented. This method is called just before transcoding, probing, or muxing starts. In avpipe library every input is determined by a url and a unique fd (the same way a file is determined by an fd in your operating system) and when open() is called the fd and the url is passed to it. Then the Open() returns an implementation of the InputHandler interface. The Read() method in InputHandler interface is used to read the input, the Seek() method is used to seek to a specific position in the input, Close() is used to close the input, Size() is used to obtain info about size of input, and Stat() is used to report some statistics of input.

```go
type InputOpener interface {
  // fd determines uniquely opening input.
  // url determines input string for transcoding
  Open(fd int64, url string) (InputHandler, error)
}

type InputHandler interface {
  // Reads from input stream into buf.
  // Returns (0, nil) to indicate EOF.
  Read(buf []byte) (int, error)

  // Seeks to a specific offset of the input.
  Seek(offset int64, whence int) (int64, error)

  // Closes the input.
  Close() error

  // Returns the size of input, if the size is not known returns 0 or -1.
  Size() int64

  // Reports some stats
  Stat(statType AVStatType, statArgs interface{}) error
}
```

#### OutputOpener interface

Similar to InputOpener this interface has only one open() method that must be implemented. This open() method is called before a new transcoding segment is generated. The new transcoding segments generated by avpipe can be HLS/DASH segments (m4s files), or fragmented MP4 files; in either case the open() method would be called before the segment is generated. This open() method has to return an implementation of the OutputHandler interface, which is used to seek, write, close and stat output segments. More specifically, in OutputHandler interface the Write() method is used to write to the output segment, the Seek() method is used to seek into the output segment, Close() method is used to close the output segment and Stat() is used to report some statistics of output.

```go
type OutputOpener interface {
  // h determines uniquely opening input.
  // fd determines uniquely opening output.
  Open(h, fd int64, stream_index, seg_index int, pts int64, out_type AVType) (OutputHandler, error)
}

type OutputHandler interface {
  // Writes encoded stream to the output.
  Write(buf []byte) (int, error)

  // Seeks to specific offset of the output.
  Seek(offset int64, whence int) (int64, error)

  // Closes the output.
  Close() error

  // Reports some stats
  Stat(avType AVType, statType AVStatType, statArgs interface{}) error
}
```

Note that the methods in InputHandler and OutputHandler interfaces are called indirectly by ffmpeg. For some examples of the implementations of these interfaces you can refer to avpipe_test.go or elvxc directory.

### Transcoding Audio/Video

Avpipe library has the following transcoding options to transcode audio/video:

- `xc_all`: in this mode both audio and video will be decoded and encoded according to transcoding params. Usually there is no need to specify decoder and decoder is detected automatically.
- `xc_video`: in this mode video will be decoded and encoded according to transcoding params.
- `xc_audio`: in this mode audio will be decoded and encoded according to encoder param (by default it is AAC).
- `xc_audio_pan`: in this mode audio pan filter will be used before injecting the audio frames into the encoder.
- `xc_audio_merge`: in this mode audio merge filter will be used before injecting the audio frames into the encoder.
- `xc_mux`: in this mode avpipe would mux some audio and video ABR segments and produce an MP4 output. In this case, it is needed to provide a mux_spec which points to ABR segments to be muxed.
- `xc_extract_images`: in this mode avpipe will extract specific images/frames at specific times from a video.

#### Audio specific params

- `channel_layout`: In all the above cases channel_layout parameter can be set to specify the output channel layout. If the channel_layout param is not set then the input channel layout would carry to the output.
- `audio_index`: The audio_index param can be used to pick the specified audio (using stream index) for transcoding.
- `audio_seg_duration_ts`: This param determines the duration of the generated audio segment in TS.
- `audio_bitrate`: This param sets the audio bitrate in the output.
- `filter_descriptor`: The filter_descriptor param must be set when transcoding type is xc_audio_pan/xc_audio_merge.

### Avpipe stat reports

- Avpipe library reports some input and output stats via some events.
- Input stats include the following events:
  - `in_stat_bytes_read`: input bytes read (this is true for all inputs but not for RTMP).
  - `in_stat_audio_frame_read`: input audio frames read so far.
  - `in_stat_video_frame_read`: input video frames read so far.
  - `in_stat_decoding_audio_start_pts`: input stream start pts for audio.
  - `in_stat_decoding_video_start_pts`: input stream start pts for video.
- Input stats are reported via input handlers avpipe_stater() callback function.
- A GO client of avpipe library, must implement InputHandler.Stat() method.
- Output stats include the following events:
  - `out_stat_bytes_written`: bytes written to current segment so far. Audio and video each have their own output segment.
  - `out_stat_frame_written`: includes total frames written and frames written to current segment.
  - `out_stat_encoding_end_pts`: end pts of generated segment. This event is generated when an output segment is complete and it is closing.
- Output stats are reported via output handlers avpipe_stater() callback function.
- A GO client of avpipe library, must implement OutputHandler.Stat() method.

### Setting up live

- Avpipe can handle HLS, UDP TS, and RTMP live streams. For each case it is needed to set parameters for live stream properly.
- If the parameters are set correctly, then avpipe recorder would read the live data and generate live audio/video mezzanine files.
- Using xc-all transcoding feature, which was added recentely, avpipe can transcode both audio and video of a live stream and produce mezzanine files.
- In order to have a good quality output, the audio and video live has to be synced.
- If input has multiple audios, avpipe can sync the selected audio with one of the elementary video streams, specified by sync_audio_to_stream_id, based the first key frame in the video stream. In this case, sync_audio_to_stream_id would be set to the stream id of the video elementary stream.

### Transcoding with preset

- Avpipe has the capability to apply preset parameter when encoding using H264 encoder.
- The experiments show that using preset `faster` instead of `medium` would generate almost the same size output/bandwidth while keeping the picture quality high.
- The other advantage of using preset `faster` instead of `medium` is that it would consume less CPU and encode faster.
- To compare the following command is used to generate the mezzanines for `creed_5_min.mov`:

```text
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

- Experimenting with other input files and some RTMP streams showed the same results.

### h.265 Implementation Notes

- h.265 levels for ingests follow the same behaviour as ffmpeg. Below are examples from our testing

| Source Resolution | Profile | Source h.265 Level | Fabric h.265 Level |
| ----------------- | ------- | ------------------ | ------------------ |
| 1080p             | Main    | 4.0                | 4.0                |
| 1080p             | Main 10 | 4.0                | 4.0                |
| 1080p             | Main    | 4.1                | 4.0                |
| 1080p             | Main 10 | 4.1                | 4.0                |
| 1080p             | Main    | 5.0                | 4.0                |
| 1080p             | Main 10 | 5.0                | 4.0                |
| 1080p             | Main    | 5.1                | 4.0                |
| 4k                | Main 10 | 5.1                | 5.0                |
| 4k                | Main 10 | 5.2                | 5.0                |
