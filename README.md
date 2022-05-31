# avpipe

# Introduction
The avpipe library is basically a C/Go library on top of FFmpeg with very simple transcoding APIs.
This library helps third party developers to develop server programs or apps for transcoding audio/video in C or Go.
The avpipe library has the capability to transcode audio/video input files and produce MP4 or M4S (for HLS/DASH playout) segments on the fly.
The source of input files for transcoding can be a file on disk, a network connection, or some objects on the cloud.
Depending on the location of the source file, the input of avpipe library can be set via some handlers (callback functions) in both C or Go.
Similarly the output of avpipe library can be set by some callback functions to send the result to a file on disk, a network connection or some objects on the cloud.

Usually the media segments are either fragmented MP4 or TS segments.
So far, the work on avpipe has been focused on generating fragmented MP4 segments (the TS format might be added later).

# Build

## Prerequisites
The following libraries must be installed:
- libx264
- libx265
- cuda-10.2 or cuda-11 (if you want to compile and build to support cuda/nvidia)

## Clone FFmpeg and avpipe
The following repositories can be checked out in any directory, but for better organization it is recommended to be cloned in the same parent directory like _src_
(_src_ is the workspace containing FFmpeg and avpipe).
 - elv-io/FFmpeg:
   - `git clone git@github.com:eluv-io/FFmpeg.git`
 - eluv-io/avpipe:
   - `git clone git@github.com:eluv-io/avpipe.git`
    
## Build FFmpeg and avpipe

- Build ffmpeg: in FFmpeg directory (_<ffmpeg-path>_) run
  - `./build.sh`
- Set environment variables: in avpipe directory run
  - `source init-env.sh <ffmpeg-path>`
- Build avpipe: in avpipe directory run
  - `make`
- This installs the binaries under _<avpipe-path>/bin_

## Test avpipe

- Download media test files from https://console.cloud.google.com/storage/browser/eluvio-test-assets into _media_ directory inside _<avpipe-path>_
  - `cd avpipe`
  - `mkdir ./media`  
  - `cd ./media`
  - `gsutil -m cp 'gs://eluvio-test-assets/*' .`
- Inside _<avpipe-path>_ run
  - `go test -timeout 2000s`
  
# Design
- Avpipe library has been built on top of the different libraries of ffmpeg, the most important ones are libx264, libx265, libavcodec, libavformat, libavfilter and libswresample. But in order to achieve all the features and capabilities some parts of ffmpeg library have been changed. Avpipe library is capable of transcoding or probing an input source (i.e a media file, or an UDP/RTMP stream) and producing output media or probe results. In order to start a transcoding job the transcoding parameters have to be set.
- This section describes some of the capabilities and features of avpipe library and how to set the parameters to utilize those features.


### Parameters

```
typedef struct xcparams_t {
    char    *url;                       // URL of the input for transcoding
    int     bypass_transcoding;         // if 0 means do transcoding, otherwise bypass transcoding
    char    *format;                    // Output format [Required, Values: dash, hls, mp4, fmp4]
    int64_t start_time_ts;              // Transcode the source starting from this time
    int64_t skip_over_pts;              // Like start_time_ts but expressed in input pts
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

    int         debug_frame_level;
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
- **Joining/merging multiple audio:** avpipe library has the capability to join and pan multiple audio input streams by setting xc_type parameter to xc_audio_join and xc_audio_pan respectively (merging multiple audio is not complete yet).
- **Using GPU:** avpipe library can utilize NVIDIA cards for transcoding. In order to utilize the NVIDIA GPU, the gpu_index must be set (the default is using GPU with index 0). To find the existing GPU indexes on a machine, nvidia-smi command can be used. In addition, the decoder and encoder should be set to "h264_cuvid" or "h264_nvenc" respectively. And finally, in order to pick the correct GPU index the following environment variable must be set “CUDA_DEVICE_ORDER=PCI_BUS_ID” before running the program.
- **Text watermarking:** this can be done with setting watermark_text, watermark_xloc, watermark_yloc, watermark_relative_sz, and watermark_font_color while transcoding a video (xc_type=xc_video), which makes specified watermark text to appear at specified location.
- **Image watermarking:** this can be done with setting watermark_overlay (the buffer containing overlay image), watermark_overlay_len, watermark_xloc, and watermark_yloc while transcoding a video (xc_type=xc_video).
- **Live streaming with UDP/HLS/RTMP:** avpipe library has the capability to transcode an input live stream and generate MP4 or ABR segments. Although the parameter setting would be similar to transcoding any other input file, setting up input/output handlers would be different (this is discussed in sections 6 and 8).
- **Extracting images:** avpipe library can extract images either using a time interval or specific timestamps. 
- **HDR support:** avpipe library allows to create HDR output while transcoding with H.265 encoder. To make an HDR content two parameters max_cll and master_display have to be set.
- **Bypass feature:** setting bypass_transcoding to 1, would avoid transcoding and copies the input packets to output. This feature is very useful (saves a lot of CPU and time) when input data matches with output and we can skip transcoding.
- **Muxing audio/video ABR segments and creating MP4 files:** this feature allows the creation of MP4 files from transcoded audio/video segments. In order to do this a muxing spec has to be made to tell avpipe which ABR segments should be stitched together to produce the final MP4. To make this feature working xc_type should be set to xc_mux and the mux_spec param should point to a buffer containing muxing spec.
- **Transcoding from specific timebase offset:** the parameter start_time_ts can be used to skip some input and transcode from specified TS in start_time_ts. This feature is also very useful to start transcoding from a certain point and not from the beginning of file/stream.
- **Audio join/pan/merge filters:**
  - setting xc_type = xc_audio_join would join 2 or more audio inputs and create a new audio output (for example joining two mono streams and creating one stereo).
  - setting xc_type = xc_audio_pan would pick different audio channels from input and create a new audio stream (for example picking different channels from a 5.1 channel layout and producing a stereo containing two channels).
  - setting xc_type = xc_audio_merge would merge different input audio streams and produce a new multi-channel output stream (for example, merging different input mono streams and create a new 5.1)
- **Debugging with frames:** if the parameter debug_frame_level is on then the logs will also include very low level debug messages to trace reading/writing every piece of data.

### Counters

- bytes read from source when completing
  - avformat_open_input
  - avformat_find_stream_info
  - decoding first packet/frame
  - producing the first encoded frame
  - make the first write to the first output segment
  - finish the first output segment

- time spent in reading source data (min/max and total time or avaerage)
- time to generating each segment

- exceptions
  - number of segments starting without a keyframe




## Design map

### C/Go interaction architecture

Avpipe library has two main layers (components): avpipe C library and avpipe C/GO library.

#### C layer:
The first layer is the C code that has been built on top of the different libraries of ffmpeg like libx264, libx265, libavcodec, libavformat, libavfilter and libswresample.
This is the main transcoding engine of avpipe and defines low level C transcoding API of avpipe library.
Avpipe uses the callbacks in avio_alloc_context() to read, write, or seek into the media files.

#### C/Go layer:
The second layer is the C/GO code, mainly avpipe.go,  that provides the API for Go programs, and avpipe.c, which glues the Go layer to avpipe C library.
The handlers in avpipe.c are the callback functions that get called by ffmpeg and they call the Go layer callback functions themselves.
This component is discussed in more detail in section 6.2.

## Avpipe stat reports
- Avpipe library reports some input and output stats via some events.
- Input stats include the following events:
  - in_stat_bytes_read: input bytes read (this is true for all inputs but not for RTMP).
  - in_stat_audio_frame_read: input audio frames read so far.
  - in_stat_video_frame_read: input video frames read so far.
  - in_stat_decoding_audio_start_pts: input stream start pts for audio.
  - in_stat_decoding_video_start_pts: input stream start pts for video.
- Input stats are reported via input handlers avpipe_stater() callback function.
- A GO client of avpipe library, must implement InputHandler.Stat() method.
- Output stats include the following events:
  - out_stat_bytes_written: bytes written to current segment so far. Audio and video each have their own output segment.
  - out_stat_frame_written: includes total frames written and frames written to current segment.
  - out_stat_encoding_end_pts: end pts of generated segment. This event is generated when an output segment is complete and it is closing.
- Output stats are reported via output handlers avpipe_stater() callback function.
- A GO client of avpipe library, must implement OutputHandler.Stat() method.

## Transcoding Audio/Video
Avpipe library has the following transcoding options to transcode audio/video:
- xc_all: in this mode both audio and video will be decoded and encoded according to transcoding params. Usually there is no need to specify decoder and decoder is detected automatically.
- xc_video: in this mode video will be decoded and encoded according to transcoding params.
- xc_audio: in this mode audio will be decoded and encoded according to encoder param (by default it is AAC).
- xc_audio_pan: in this mode audio pan filter will be used before injecting the audio frames into the encoder.
- xc_audio_merge: in this mode audio merge filter will be used before injecting the audio frames into the encoder.
- xc_mux: in this mode avpipe would mux some audio and video ABR segments and produce an MP4 output. In this case, it is needed to provide a mux_spec which points to ABR segments to be muxed.
- xc_extract_images: in this mode avpipe will extract specific images/frames at specific times from a video.

### Audio specific params
- channel_layout: In all the above cases channel_layout parameter can be set to specify the output channel layout. If the channel_layout param is not set then the input channel layout would carry to the output.
- audio_index: The audio_index param can be used to pick the specified audio (using stream index) for transcoding. 
- audio_seg_duration_ts: This param determines the duration of the generated audio segment in TS.
- audio_bitrate: This param sets the audio bitrate in the output.
- filter_descriptor: The filter_descriptor param must be set when transcoding type is xc_audio_pan/xc_audio_merge.


## Setting up live

- Avpipe can handle HLS, UDP TS, and RTMP live streams. For each case it is needed to set parameters for live stream properly.
- If the parameters are set correctly, then avpipe recorder would read the live data and generate live audio/video mezzanine files.
- Using xc-all transcoding feature, which was added recentely, avpipe can transcode both audio and video of a live stream and produce mezzanine files.
- In order to have a good quality output, the audio and video live has to be synced.
- If input has multiple audios, avpipe can sync the selected audio with one of the elementary video streams, specified by sync_audio_to_stream_id, based the first key frame in the video stream. In this case, sync_audio_to_stream_id would be set to the stream id of the video elementary stream.


## Transcoding with preset

- Avpipe has the capability to apply preset parameter when encoding using H264 encoder.
- The experiments show that using preset `faster` instead of `medium` would generate almost the same size output/bandwidth while keeping the picture quality high.
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


## h.265 Implementation Notes
- h.265 levels for ingests follow the same behaviour as ffmpeg. Below are examples from our testing

|Source Resolution|Profile|Source h.265 Level|Fabric h.265 Level|
|-----------------|-------|------------------|------------------|
|1080p            |Main   |4.0               |4.0               |
|1080p            |Main 10|4.0               |4.0               |
|1080p            |Main   |4.1               |4.0               |
|1080p            |Main 10|4.1               |4.0               |
|1080p            |Main   |5.0               |4.0               |
|1080p            |Main 10|5.0               |4.0               |
|1080p            |Main   |5.1               |4.0               |
|4k               |Main 10|5.1               |5.0               |
|4k               |Main 10|5.2               |5.0               |
