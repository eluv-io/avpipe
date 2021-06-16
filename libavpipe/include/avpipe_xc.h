/*
 * avpipe_xc.h
 */

#ifndef AVPIPE_XC_H
#define AVPIPE_XC_H
#pragma once

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libswresample/swresample.h>
#include <libavutil/audio_fifo.h>
#include <libavutil/opt.h>

#include <pthread.h>
#include "elv_channel.h"

#define MAX_STREAMS	        64
#define MAX_MUX_IN_STREAM   4096
#define MAX_AUDIO_MUX       8
#define MAX_CAPTION_MUX     8

/*
 * Adding/deleting an error code needs adding/deleting corresponding GO
 * error in avpipe_errors.go
 */
typedef enum avpipe_error_t {
    eav_success                 = 0,    // No error
    eav_filter_string_init      = 1,    // Error in initializing filter string
    eav_mem_alloc               = 2,    // Error in allocating memory or an ffmpeg context
    eav_filter_init             = 3,    // Error in initializing filter
    eav_num_streams             = 4,    // Bad number of audio/video inputs
    eav_write_header            = 5,    // Error in writing headers
    eav_timebase                = 6,    // Timebase mismatch
    eav_seek                    = 7,    // Error in seeking input
    eav_cancelled               = 8,    // The transcoding cancelled
    eav_open_input              = 9,    // Error in opening input stream
    eav_stream_info             = 10,   // Error in obtaining stream info
    eav_codec_context           = 11,   // Error in allocating decoder/encoder context
    eav_codec_param             = 12,   // Bad codec parameter
    eav_open_codec              = 13,   // Error in opening decoder/encoder
    eav_param                   = 14,   // Bad avpipe parameter
    eav_stream_index            = 15,   // Bad stream index in input/output packet
    eav_read_input              = 16,   // Error in reading input frames
    eav_send_packet             = 17,   // Error in sending packet to the decoder
    eav_receive_frame           = 18,   // Error in receiving frame from decoder or audio fifo
    eav_receive_filter_frame    = 19,   // Error in receiving frame from filter buffer sink
    eav_receive_packet          = 20,   // Error in receiving packet from encoder
    eav_write_frame             = 21,   // Error in writing frame to output stream or audio fifo
    eav_audio_sample            = 22,   // Error in converting audio samples
    eav_xc_table                = 23
} avpipe_error_t;

typedef enum avpipe_buftype_t {
    avpipe_input_stream = 0,
    avpipe_manifest = 1,                // dash.mpd
    avpipe_video_init_stream = 2,       // video init_stream
    avpipe_audio_init_stream = 3,       // audio init_stream
    avpipe_video_segment = 4,           // video ABR segment
    avpipe_audio_segment = 5,           // audio ABR segment
    avpipe_master_m3u = 6,              // hls master m3u
    avpipe_video_m3u = 7,               // video m3u
    avpipe_audio_m3u = 8,               // audio m3u
    avpipe_aes_128_key = 9,             // AES key
    avpipe_mp4_stream = 10,             // mp4 stream
    avpipe_fmp4_stream = 11,            // fragmented mp4 stream
    avpipe_mp4_segment = 12,            // segmented mp4 stream
    avpipe_video_fmp4_segment = 13,     // segmented fmp4 video stream
    avpipe_audio_fmp4_segment = 14,     // segmented fmp4 audio stream
    avpipe_image = 15                   // extracted images
} avpipe_buftype_t;

#define BYTES_READ_REPORT   (20*1024*1024)

typedef enum avp_stat_t {
    in_stat_bytes_read = 1,
    out_stat_bytes_written = 2,
    out_stat_decoding_start_pts = 4,
    out_stat_encoding_end_pts = 8
} avp_stat_t;

struct coderctx_t;

#define MAX_UDP_PKT_LEN         2048            /* Max UDP length */
#define UDP_PIPE_TIMEOUT        60              /* sec */
#define UDP_PIPE_BUFSIZE        (128*1024*1024) /* 128MB recv buf size */
#define MAX_UDP_CHANNEL         100000          /* Max # of entries in UDP channel */

typedef struct udp_packet_t {
    char buf[MAX_UDP_PKT_LEN];
    int len;
} udp_packet_t;

typedef struct mux_input_ctx_t {
    int     n_parts;                    /* Number of input parts */
    int     index;                      /* Index of current input part that should be processed */
    char    *parts[MAX_MUX_IN_STREAM];  /* All the input parts */
    int     header_size;
} mux_input_ctx_t;

typedef struct io_mux_ctx_t {
    char            *out_filename;              /* Output filename/url for this muxing */
    char            *mux_type;                  /* "mux-mez" or "mux-abr" */
    mux_input_ctx_t video;
    int64_t         last_video_pts;
    int             last_audio_index;
    mux_input_ctx_t audios[MAX_AUDIO_MUX];
    int64_t         last_audio_pts;
    int             last_caption_index;
    mux_input_ctx_t captions[MAX_CAPTION_MUX];
} io_mux_ctx_t;

typedef struct ioctx_t {
    /* Application specific IO context */
    void                *opaque;
    struct coderctx_t   *encoder_ctx;   /* Needed to get access for stats */
    elv_channel_t       *udp_channel;   /* This is set if input is a UDP url */
    udp_packet_t        *cur_packet;    /* Current UDP packet not consumed fully */
    int                 cur_pread;      /* Current packet read */
    pthread_t           utid;           /* UDP thread id */

    /* Input filename or url */
    char                *url;

    avpipe_buftype_t    type;
    unsigned char*      buf;
    int                 bufsz;

    /* Size of input, should be set in in_handler-> avpipe_opener_f() */
    int64_t sz;

    /* Read/write counters, used by input/output handlers */
    int64_t read_bytes;
    int64_t read_pos;
    int64_t read_reported;
    int64_t written_bytes;
    int64_t write_pos;
    int64_t write_reported;

    /* Audio/video decoding start pts for stat reporting */
    int64_t decoding_start_pts;

    /* Output handlers specific data */
    int stream_index;       /* usually (but not always) video=0 and audio=1 */
    int seg_index;          /* segment index if this ioctx is a segment */

    io_mux_ctx_t    *in_mux_ctx;   /* Input muxer context */
    int             in_mux_index;

    /* Pointer to input context of a transcoding session.
     * A transcoding session has one input (i.e one mp4 file) and
     * multiple output (i.e multiple segment files, dash and init_stream files).
     */
    struct ioctx_t *inctx;
} ioctx_t;

typedef int
(*avpipe_opener_f)(
    const char *url,
    ioctx_t *ioctx);

typedef int
(*avpipe_closer_f)(
    ioctx_t *ioctx);

typedef int
(*avpipe_reader_f)(
    void *opaque,
    uint8_t *buf,
    int buf_size);

typedef int
(*avpipe_writer_f)(
    void *opaque,
    uint8_t *buf,
    int buf_size);

typedef int64_t
(*avpipe_seeker_f)(
    void *opaque,
    int64_t offset,
    int whence);

typedef int
(*avpipe_stater_f)(
    void *opaque,
    avp_stat_t stat_type);

typedef struct avpipe_io_handler_t {
    avpipe_opener_f avpipe_opener;
    avpipe_closer_f avpipe_closer;
    avpipe_reader_f avpipe_reader;
    avpipe_writer_f avpipe_writer;
    avpipe_seeker_f avpipe_seeker;
    avpipe_stater_f avpipe_stater;
} avpipe_io_handler_t;

#define MAX_WRAP_PTS    ((int64_t)8589000000)

/* Decoder/encoder context, keeps both video and audio stream ffmpeg contexts */
typedef struct coderctx_t {
    AVFormatContext     *format_context;        /* Input format context or video output format context */
    AVFormatContext     *format_context2;       /* Audio output format context */

    AVCodec             *codec[MAX_STREAMS];
    AVStream            *stream[MAX_STREAMS];
    AVCodecParameters   *codec_parameters[MAX_STREAMS];
    AVCodecContext      *codec_context[MAX_STREAMS];
    SwrContext          *resampler_context;             /* resample context for audio */
    AVAudioFifo         *fifo;                          /* audio sampling fifo */

    int video_stream_index;
    int audio_stream_index[MAX_AUDIO_MUX];              /* Audio input stream indexes */
    int n_audio;                                        /* Number of audio streams that will be decoded */

    int data_stream_index;
    int audio_enc_stream_index;                         /* Audio output stream index */

    int64_t video_last_wrapped_pts;                     /* Video last wrapped pts */
    int64_t video_last_input_pts;                       /* Video last input pts */
    int64_t audio_last_wrapped_pts;                     /* Audio last wrapped pts */
    int64_t audio_last_input_pts;                       /* Audio last input pts */
    int64_t video_last_dts;
    int64_t audio_last_dts;
    int64_t last_key_frame;                             /* pts of last key frame */
    int64_t forced_keyint_countdown;                    /* frames until next forced key frame */
    int64_t video_last_pts_read;                        /* Video input last pts read */
    int64_t audio_last_pts_read;                        /* Audio input last pts reas */
    int64_t video_last_pts_sent_encode;                 /* Video last pts to encode if tx_type & tx_video */
    int64_t audio_last_pts_sent_encode;                 /* Audio last pts to encode if tx_type & tx_audio */
    int64_t video_last_pts_encoded;                     /* Video last input pts encoded if tx_type & tx_video */
    int64_t audio_last_pts_encoded;                     /* Audio last input pts encoded if tx_type & tx_audio */

    int64_t audio_output_pts;                           /* Used to set PTS directly when using audio FIFO */

    /* Video filter */
    AVFilterContext *video_buffersink_ctx;
    AVFilterContext *video_buffersrc_ctx;
    AVFilterGraph   *video_filter_graph;

    /* Audio filter */
    AVFilterContext *audio_buffersink_ctx;
    AVFilterContext *audio_buffersrc_ctx[MAX_AUDIO_MUX];
    AVFilterGraph   *audio_filter_graph;

    int64_t video_pts;                                  /* Video decoder/encoder pts */
    int64_t audio_pts;                                  /* Audio decoder/encoder pts */
    int64_t video_input_start_pts;                      /* In case video input stream starts at PTS > 0 */
    int64_t audio_input_start_pts;                      /* In case audio input stream starts at PTS > 0 */
    int64_t first_decoding_video_pts;                   /* PTS of first video frame read from the decoder */
    int64_t first_decoding_audio_pts;                   /* PTS of first audio frame read from the decoder */
    int64_t first_encoding_video_pts;                   /* PTS of first video frame sent to the encoder */
    int64_t first_encoding_audio_pts;                   /* PTS of first audio frame sent to the encoder */
    int64_t first_read_frame_pts[MAX_STREAMS];          /* PTS of first frame read - which might not be decodable */

    int64_t audio_input_prev_pts;       /* Previous pts for audio input */
    int64_t video_encoder_prev_pts;     /* Previous pts for video output (encoder) */
    int64_t video_duration;             /* Duration/pts of original frame */
    int64_t audio_duration;             /* Audio duration/pts of original frame when tx_type == tx_all */
    int64_t first_key_frame_pts;        /* First video key frame pts, used to synchronize audio and video in UDP live streams */
    int     pts_residue;                /* Residue of pts lost in output */

    int     is_rtmp;
    int     is_mpegts;                  /* Set to 1 if input format name is "mpegts" */
    int     mpegts_synced;              /* will be set to 1 if audio and video are synced */
    int     frame_duration;             /* Will be > 0 if parameter set_equal_fduration is set and doing mez making */
    int     calculated_frame_duration;  /* Approximate/real frame duration of video stream, will be used to fill video frames */

    int     cancelled;
    int     stopped;
} coderctx_t;

typedef enum crypt_scheme_t {
    crypt_none,
    crypt_aes128,
    crypt_cenc,
    crypt_cbc1,
    crypt_cens,
    crypt_cbcs
} crypt_scheme_t;

typedef enum tx_type_t {
    tx_none           = 0,
    tx_video          = 1,
    tx_audio          = 2,
    tx_all            = 3,    // tx_video | tx_audio
    tx_audio_merge    = 6,    // 0x04 | tx_audio
    tx_audio_join     = 10,   // 0x08 | tx_audio
    tx_audio_pan      = 18,   // 0x10 | tx_audio
    tx_mux            = 32,
    tx_extract_images = 65
} tx_type_t;

/* handled image types in get_overlay_filter_string*/
typedef enum image_type {
    unknown_image,
    png_image,
    jpg_image,
    gif_image
} image_type;

#define DRAW_TEXT_SHADOW_OFFSET     0.075

typedef struct txparams_t {
    int     bypass_transcoding;     // if 0 means do transcoding, otherwise bypass transcoding (only copy)
    char    *format;                // Output format [Required, Values: dash, hls, mp4, fmp4]
    int64_t start_time_ts;          // Transcode the source starting from this time
    int64_t skip_over_pts;          // Like start_time_ts but expressed in input pts
    int64_t start_pts;              // Starting PTS for output
    int64_t duration_ts;            // Transcode time period [-1 for entire source length from start_time_ts]
    char    *start_segment_str;     // Specify index of the first segment  TODO: change type to int
    int     video_bitrate;
    int     audio_bitrate;
    int     sample_rate;            // Audio sampling rate
    char    *crf_str;
    char    *preset;                // Sets encoding speed to compression ratio
    int     rc_max_rate;            // Rate control - max rate
    int     rc_buffer_size;         // Rate control - buffer size
    int64_t audio_seg_duration_ts;  // In ts units. It is used for transcoding and producing audio ABR/mez segments
    int64_t video_seg_duration_ts;  // In ts units. It is used for transcoding and producing video ABR/mez segments 
    char    *seg_duration;          // In sec units. It is used for transcoding and producing mp4 segments
    int     seg_duration_fr;
    int     start_fragment_index;
    int     force_keyint;           // Force a key (IDR) frame at this interval
    int     force_equal_fduration;  // Force all frames to have equal frame duration 
    char    *ecodec;                // Video encoder
    char    *ecodec2;               // Audio encoder when tx_type & tx_audio
    char    *dcodec;                // Video decoder
    char    *dcodec2;               // Audio decoder when tx_type & tx_audio
    int     gpu_index;              // GPU index for transcoding, must be >= 0
    int     enc_height;
    int     enc_width;
    char    *crypt_iv;              // 16-byte AES IV in hex [Optional, Default: Generated]
    char    *crypt_key;             // 16-byte AES key in hex [Optional, Default: Generated]
    char    *crypt_kid;             // 16-byte UUID in hex [Optional, required for CENC]
    char    *crypt_key_url;         // Specify a key URL in the manifest [Optional, Default: key.bin]
    int     skip_decoding;          // If set, then skip the packets until start_time_ts without decoding

    crypt_scheme_t  crypt_scheme;   // Content protection / DRM / encryption [Optional, Default: crypt_none]
    tx_type_t       tx_type;        // Default: 0 means transcode 'everything'

    int         seekable;                   // Default: 0 means not seekable. A non seekable stream with moov box in
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

    int         audio_index[MAX_AUDIO_MUX]; // Audio index(s) for mez making, may need to become an array of indexes
    int         n_audio;                    // Number of entries in audio_index
    int         audio_fill_gap;             // Audio only, fills the gap if there is a jump in PTS
    int         sync_audio_to_stream_id;    // mpegts only, default is 0
    int         bitdepth;                   // Can be 8, 10, 12
    char        *max_cll;                   // Maximum Content Light Level (HDR only)
    char        *master_display;            // Master display (HDR only)
    int         stream_id;                  // Stream id to trasncode, should be >= 0
    char        *filter_descriptor;         // Filter descriptor if tx-type == audio-merge
    char        *mux_spec;
    int64_t     extract_image_interval_ts;  // Write frames at this interval. Default: -1 (use source timescale * 10)
} txparams_t;

#define MAX_CODEC_NAME  256

typedef struct stream_info_t {
    int         stream_index;       // Stream index in AVFormatContext
    int         stream_id;          // Format-specific stream ID, set by libavformat during decoding
    int         codec_type;         // Audio or Video
    int         codec_id;
    char        codec_name[MAX_CODEC_NAME+1];
    int64_t     duration_ts;
    AVRational  time_base;
    int64_t     nb_frames;
    int64_t     start_time;
    AVRational  avg_frame_rate;
    AVRational  frame_rate;         // Same as r_frame_rate
    int         sample_rate;        // Audio only, samples per second
    int         channels;           // Audio only, number of audio channels
    int         channel_layout;     // Audio channel layout
    int         ticks_per_frame;
    int64_t     bit_rate;
    int         has_b_frames;
    int         width, height;       // Video only

    enum AVPixelFormat  pix_fmt;     // Video only

    AVRational          sample_aspect_ratio;
    AVRational          display_aspect_ratio;
    enum AVFieldOrder   field_order;
    int                 profile;
    int                 level;
} stream_info_t;

typedef struct container_info_t {
    float duration;
    char *format_name;
} container_info_t;

/* The data structure that is filled by avpipe_probe */
typedef struct txprobe_t {
    container_info_t container_info;
    stream_info_t *stream_info;    // An array of stream_info_t (usually 2)
} txprobe_t;

typedef struct txctx_t {
    coderctx_t          decoder_ctx;
    coderctx_t          encoder_ctx;
    txparams_t          *params;
    int32_t             index;  // index in tx table
    int32_t             handle; // handle for V2 API
    ioctx_t             *inctx;
    avpipe_io_handler_t *in_handlers;
    avpipe_io_handler_t *out_handlers;
    int                 debug_frame_level;
    int                 do_instrument;

    /*
     * Data structures that are needed for muxing multiple inputs to generate one output.
     * Each video/audio/caption input stream can have multiple input files/parts.
     * Each video/audio/caption input stream has its own coderctx_t and ioctx_t.
     */
    io_mux_ctx_t        *in_mux_ctx;                                        // Input muxer context
    coderctx_t          in_muxer_ctx[MAX_AUDIO_MUX+MAX_CAPTION_MUX+1];      // Video, audio, captions coder input muxer context (one video, multiple audio/caption)
    ioctx_t             *inctx_muxer[MAX_AUDIO_MUX+MAX_CAPTION_MUX+1];      // Video, audio, captions io muxer context (one video, multiple audio/caption)
    coderctx_t          out_muxer_ctx;                                      // Output muxer

    AVPacket            pkt_array[MAX_AUDIO_MUX+MAX_CAPTION_MUX+1];
    int                 is_pkt_valid[MAX_AUDIO_MUX+MAX_CAPTION_MUX+1];

    elv_channel_t       *vc;        // Video frame channel
    elv_channel_t       *ac;        // Audio frame channel
    pthread_t           vthread_id;
    pthread_t           athread_id;
    int                 stop;
    int                 err;        // Return code of transcoding
} txctx_t;

/* Params that are needed to decode/encode a frame in a thread */
typedef struct xc_frame_t {
    AVPacket    *packet;
    AVFrame     *frame;
    AVFrame     *filt_frame;
    int         stream_index;
} xc_frame_t;

typedef struct out_tracker_t {
    struct avpipe_io_handler_t  *out_handlers;
    coderctx_t                  *encoder_ctx;   /* Needed to get access for stats */
    ioctx_t                     *last_outctx;
    int64_t                     seg_index;
    ioctx_t                     *inctx;         /* Points to input context */
    tx_type_t                   tx_type;

    /** Needed to detect type of encoding frame */
    int video_stream_index;
    int audio_stream_index;
} out_tracker_t;

/**
 * @brief   Allocates and initializes a txctx_t (transcoder context) for piplining the input stream.
 *          In case of failure avpipe_fini() should be called to avoid resource leak.
 *
 * @param   txctx           Points to allocated and initialized memory (different fields are initialized by ffmpeg).
 * @param   in_handlers     A pointer to input handlers. Must be properly set up by the application.
 * @param   inctx           A pointer to ioctx_t for input stream. This has to be allocated and initialized
 *                          by the application before calling this function.
 * @param   out_handlers    A pointer to output handlers. Must be properly set up by the application.
 * @param   params          A pointer to the parameters for transcoding.
 * @param   url             Points to input url or filename.
 *
 * @return  Returns 0 if the initialization of an avpipe txctx_t is successful, otherwise returns corresponding eav error.
 */
int
avpipe_init(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    avpipe_io_handler_t *out_handlers,
    txparams_t *params,
    char *url);

/**
 * @brief   Frees the memory and other resources allocated by ffmpeg.
 *
 * @param   txctx       A pointer to the trascoding context that would be destructed.
 * @return  Returns 0.
 */
int
avpipe_fini(
    txctx_t **txctx);

/*
 * @brief   Returns channel layout name.
 *
 * @param   nb_channels     Number of channels.
 * @param   channel_layout  Channel layout id.
 *
 * @return  Returns channel layout name if it can find channel layout with corresponding layout id, otherwise empty string.
 */
const char*
avpipe_channel_name(
    int nb_channels,
    int channel_layout);


/**
 * @brief   Probes object stream specified by input handler.
 *
 * @param   in_handlers     A pointer to input handlers that direct the probe
 * @param   inctx           A pointer to ioctx_t for input stream. This has to be allocated and initialized
 *                          by the application before calling this function.
 * @param   seekable        A flag to specify whether input stream is seakable or no
 * @param   txprob          A pointer to the txprobe_t that could contain probing info.
 * @param   n_streams       Will contail number of streams that are probed if successful.
 * @return  Returns 0 if successful, otherwise corresponding eav error.
 */
int
avpipe_probe(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    int seekable,
    txprobe_t **txprobe,
    int *n_streams);

/**
 * @brief   Starts transcoding.
 *          In case of failure avpipe_fini() should be called to avoid resource leak.
 *
 * @param   txctx               A pointer to transcoding context.
 * @param   do_intrument        If 0 there will be no instrumentation, otherwise it does some instrumentation
 *                              for some ffmpeg functions.
 * @return  Returns 0 if transcoding is successful, otherwise -1.
 */
int
avpipe_xc(
    txctx_t *txctx,
    int do_instrument,
    int debug_frame_level);

/**
 * @brief   Initializes the avpipe muxer.
 *
 * @param   txctx           A pointer to a transcoding context.
 * @param   in_handlers     A pointer to input handlers. Must be properly set up by the application.
 * @param   out_handlers    A pointer to output handlers. Must be properly set up by the application.
 * @param   params          A pointer to the parameters for transcoding/muxing.
 * @param   url             Output url name (filename)
 * @return  Returns 0 if initializing the muxer is successful, otherwise -1.
 */
int
avpipe_init_muxer(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    io_mux_ctx_t *in_mux_ctx,
    avpipe_io_handler_t *out_handlers,
    txparams_t *params,
    char *url);

/**
 * @brief   Frees the memory and other resources allocated by avpipe muxer/ffmpeg.
 *
 * @param   txctx       A pointer to the trascoding context that would be destructed.
 * @return  Returns 0.
 */
int
avpipe_mux_fini(
    txctx_t **txctx);

/**
 * @brief   Starts avpipe muxer.
 *
 * @param   txctx           A pointer to a transcoding context.
 * @return  Returns 0 if muxing is successful, otherwise -1.
 */
int
avpipe_mux(
    txctx_t *txctx);

/**
 * @brief   Returns avpipe GIT version
 */
char *
avpipe_version();

#endif
