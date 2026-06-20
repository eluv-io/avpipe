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
#define MAX_MUX_IN_STREAM   (4*4096)        // Up to 4*4096 ABR segments

#define AVIO_OUT_BUF_SIZE   (1*1024*1024)   // avio output buffer size
#define AVIO_IN_BUF_SIZE    (1*1024*1024)   // avio input buffer size
#define MAX_URL_SIZE        1024            // Maximum URL size

//#define DEBUG_UDP_PACKET  // Uncomment for development, debugging and testing

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
    eav_xc_table                = 23,   // Error in trancoding table
    eav_pts_wrapped             = 24,   // PTS wrapped error
    eav_io_timeout              = 25,   // IO timeout
    eav_bad_handle              = 26    // Bad handle
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
    avpipe_mux_segment = 15,            // Muxed audio/video segment
    avpipe_image = 16,                  // extracted images
    avpipe_mpegts_segment = 17          // MPEGTS (muxed audio and video)
} avpipe_buftype_t;

#define BYTES_READ_REPORT               (10*1024*1024)
#define VIDEO_BYTES_WRITE_REPORT        (1024*1024)
#define AUDIO_BYTES_WRITE_REPORT        (64*1024)

typedef enum avp_stat_t {
    in_stat_bytes_read = 1,                 // # of bytes read from input stream
    in_stat_audio_frame_read = 2,           // # of audio frames read from the input stream
    in_stat_video_frame_read = 3,           // # of video frames read from the input stream
    in_stat_decoding_audio_start_pts = 4,   // PTS of first audio packet went to the decoder
    in_stat_decoding_video_start_pts = 5,   // PTS of first video packet went to the decoder
    out_stat_bytes_written = 6,             // # of bytes written to the output stream
    out_stat_frame_written = 7,             // # of frames written to the output stream
    in_stat_first_keyframe_pts = 8,         // First keyframe in the input stream
    out_stat_encoding_end_pts = 9,          // The last PTS encoded. This stat is recorded when a file is closed
    out_stat_start_file = 10,               // Sent when a new file is opened and reports the segment index
    out_stat_end_file = 11,                 // Sent when a file is closed and reports the segment index
    in_stat_data_scte35 = 12               // SCTE data arrived
} avp_stat_t;

typedef enum avp_live_proto_t {
    avp_proto_none   = 0,
    avp_proto_mpegts = 1,
    avp_proto_rtmp   = 2,
    avp_proto_srt    = 3,
    avp_proto_rtp    = 4
} avp_live_proto_t;

typedef enum avp_container_t {
    avp_container_none   = 0,
    avp_container_mpegts = 1, // MPEGTS container - can be encapsulated in MPEGTS, SRT, RTP
    avp_container_flv    = 2  // FLV container - can be encapsluated in RTMP
} avp_container_t;

struct coderctx_t;

#define MAX_UDP_PKT_LEN         2048            /* Max UDP length */
#define UDP_PIPE_TIMEOUT        5               /* sec */
#define UDP_PIPE_BUFSIZE        (128*1024*1024) /* 128MB recv buf size */
#define MAX_UDP_CHANNEL         100000          /* Max # of entries in UDP channel */

typedef struct udp_packet_t {
    char buf[MAX_UDP_PKT_LEN];
    int len;
    int pkt_num;
} udp_packet_t;

typedef struct mux_input_ctx_t {
    int     n_parts;                    /* Number of input parts */
    int     index;                      /* Index of current input part that should be processed */
    char    **parts;                    /* All the input parts */
    int     header_size;
} mux_input_ctx_t;

/*
io_mux_ctx_t is used for handling input streams for muxing. It assumes that all input streams, as
ordered by the muxing spec, have the ordering video -> audio(s) -> caption(s). It stores an input
context for each of the input streams, as well as the number of each.
*/
typedef struct io_mux_ctx_t {
    char            *out_filename;              /* Output filename/url for this muxing */
    char            *mux_type;                  /* "mux-mez" or "mux-abr" */
    mux_input_ctx_t video;
    int64_t         last_video_pts;
    int             audio_count;
    mux_input_ctx_t audios[MAX_STREAMS];
    int64_t         last_audio_pts;
    int             caption_count;
    mux_input_ctx_t captions[MAX_STREAMS];
} io_mux_ctx_t;

typedef struct xcparams_t xcparams_t;

typedef struct ioctx_t {
    /* Application specific IO context */
    void                *opaque;
    struct coderctx_t   *encoder_ctx;   /* Needed to get access for stats */
    elv_channel_t       *udp_channel;   /* This is set if input is a UDP url */
    udp_packet_t        *cur_packet;    /* Current UDP packet not consumed fully */
    int                 is_udp_started; /* Is the first UDP read started? */
    int                 cur_pread;      /* Current packet read */
    pthread_t           utid;           /* UDP thread id */

    /* Input filename or url */
    char                *url;
    char                *alt_url;   /* Alternate URL for ffmpeg (e.g. rtp:// rewritten as udp://) */

    avpipe_buftype_t    type;
    unsigned char*      buf;
    int                 bufsz;		/* Buffer size for IO */

    /* Size of input, should be set in in_handler-> avpipe_opener_f() */
    int64_t sz;

    /* Read/write counters, used by input/output handlers */
    int64_t read_bytes;
    int64_t read_pos;
    int64_t read_reported;
    int64_t written_bytes;
    int64_t write_pos;
    int64_t write_reported;
    int64_t frames_written;         /* Frames written in current segment */
    int64_t total_frames_written;   /* Total frames written */
    int64_t audio_frames_read;      /* Total audio frames read from input */
    int64_t video_frames_read;      /* Total video frames read from input */

    /* Audio/video decoding start pts for stat reporting */
    int64_t decoding_start_pts;
    int64_t first_key_frame_pts;

    /* Output handlers specific data */
    int64_t pts;                /* frame pts */
    int     stream_index;       /* usually (but not always) video=0 and audio=1 */
    int     seg_index;          /* segment index if this ioctx is a segment */

    uint8_t *data;  /* Data stream buffer (e.g. SCTE-35) */

    io_mux_ctx_t    *in_mux_ctx;   /* Input muxer context */
    int             in_mux_index;

    /* Pointer to input context of a transcoding session.
     * A transcoding session has one input (i.e one mp4 file) and
     * multiple output (i.e multiple segment files, dash and init_stream files).
     */
    struct ioctx_t  *inctx;

    xcparams_t      *params;

    volatile int    closed; /* If it is set that means inctx is closed */
} ioctx_t;

typedef struct h264_level_descriptor {
    const char *name;
    uint8_t     level_idc;
    uint8_t     constraint_set3_flag;
    uint32_t    max_mbps;
    uint32_t    max_fs;
    uint32_t    max_dpb_mbs;
    uint32_t    max_br;
    uint32_t    max_cpb;
    uint16_t    max_v_mv_r;
    uint8_t     min_cr;
    uint8_t     max_mvs_per_2mb;
} h264_level_descriptor;

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
    const uint8_t *buf,
    int buf_size);

typedef int64_t
(*avpipe_seeker_f)(
    void *opaque,
    int64_t offset,
    int whence);

typedef int
(*avpipe_stater_f)(
    void *opaque,
    int stream_index,           /* The stream_index is not valid for input stat in_stat_bytes_read. */
    avp_stat_t stat_type);

typedef struct avpipe_io_handler_t {
    avpipe_opener_f avpipe_opener;
    avpipe_closer_f avpipe_closer;
    avpipe_reader_f avpipe_reader;
    avpipe_writer_f avpipe_writer;
    avpipe_seeker_f avpipe_seeker;
    avpipe_stater_f avpipe_stater;
} avpipe_io_handler_t;

#define MAX_WRAP_PTS        ((int64_t)8589000000)
#define MAX_AVFILENAME_LEN  128

/*
 * Decoder/encoder context, keeps both video and audio stream ffmpeg contexts
 *
 * This structure supports:
 *   - one video stream (at most) - video_stream_index
 *   - one or more audio streams - audio_stream_index[]
 *   - one SCTE-35 data stream    - data_scte35_stream_index
 *   - one arbitrary data stream  - data_stream_index (not used currently)
 *
 * The caller specifies which source media audio streams to encode, using xc_params->audio_index, eg.
 *   - audio_index[0] = 1;
 *   - audio_index[1] = 3;
 *   - audio_index[2] = 4;
 *   (and xc->params->n_audio is 3)
 *
 * Audio stream index mapping is stored as follows:
 *
 * - decoder
 *   - the audio_stream_index array stores the selected stream index values the same way as xc_params
 *     - audio_stream_index[0] = 1;
 *     - audio_stream_index[1] = 3;
 *     - audio_stream_index[2] = 4;
 *     (and the number of streams is stored in 'n_audio')
 *
 * - encoder
 *   - if the encoding operation is audio join, merge or pan (which effectively takes multiple input steams and makes one output stream)
 *      - audio_stream_index[0] = 0; (output stream index is considered 0 and nb_audio_output is 1)
 *   - otherwise it uses a strange convention (needs fixed - this is impossible to traverse)
 *      - audio_stream_index[0] unset
 *      - audio_stream_index[1] = 1
 *      - audio_stream_index[2] unset
 *      - audio_stream_index[3] = 3
 *      - audio_stream_index[4] = 4
 *
 * The video format context is stored in 'format_context'
 * Audio format contexts for each audio output is stored in 'format_context2[]'
 *   - this array is contiguous and has 'n_audio' elements eg. for the xc_params above
 *     - format_context2[0] is the context for audio stream index 1
 *     - format_context2[1] is the context for audio stream index 3
 *     - format_context2[2] is the context for audio stream index 4
 *
 * Codec contexts (AVCodecContext) are stored in 'codec_context[]' as follows:
 *
 * - decoder
 *   - the codec_context array is indexed using the source media stream index values, eg. for the xc_params above
 *     - codec_context[0]  video  (if the source has video on stream_index 0, for example)
 *     - codec_context[1]  audio stream index 1
 *     - codec_context[2]  audio stream index 2 (not selected, per xc_params->audio_index)
 *     - codec_context[3]  audio stream index 3
 *     - codec_context[4]  audio stream index 4
 *
 * - encoder
 *   - if the encoding operation is audio join, merge or pan
 *     - codec_context[0]  is the codec context for the one output audio stream
 *   - otherwise the array is indexed the same way as the encoder 'audio_stream_index' array, eg.
 *      - codec_context[0] codec context for the video stream
 *      - codec_context[1] codec context for audio stream index 1
 *      - codec_context[2] unset
 *      - codec_context[3] codec context for audio stream index 3
 *      - codec_context[4] codec context for audio stream index 4
 */
typedef struct coderctx_t {
    AVFormatContext     *format_context;                                /* Input format context or video output format context */
    AVFormatContext     *format_context2[MAX_STREAMS];                  /* Audio output format context, indexed by audio index */
    char                filename2[MAX_STREAMS][MAX_AVFILENAME_LEN];     /* Audio filename formats */
    int                 n_audio_output;                                 /* Number of audio output streams, it is set for encoder */

    const AVCodec       *codec[MAX_STREAMS];
    AVStream            *stream[MAX_STREAMS];
    AVCodecParameters   *codec_parameters[MAX_STREAMS];
    AVCodecContext      *codec_context[MAX_STREAMS];    /* Audio/video AVCodecContext, indexed by stream_index */
    SwrContext          *resampler_context;             /* resample context for audio */
    AVAudioFifo         *fifo;                          /* audio sampling fifo */

    avpipe_io_handler_t *in_handlers;
    avpipe_io_handler_t *out_handlers;
    ioctx_t             *inctx;                         /* Input context needed for stat callbacks */

    int video_stream_index;
    int audio_stream_index[MAX_STREAMS];                /* Audio input stream indexes */
    int n_audio;                                        /* Number of audio streams that will be decoded */

    int data_scte35_stream_index;                       /* Index of SCTE-35 data stream */
    int data_stream_index;                              /* Index of an unrecognized data stream */

    int64_t video_last_wrapped_pts;                     /* Video last wrapped pts */
    int64_t video_last_input_pts;                       /* Video last input pts */
    int64_t audio_last_wrapped_pts[MAX_STREAMS];        /* Audio last wrapped pts */
    int64_t audio_last_input_pts[MAX_STREAMS];          /* Audio last input pts */
    int64_t video_last_dts;
    int64_t audio_last_dts[MAX_STREAMS];
    int64_t last_key_frame;                             /* pts of last key frame */
    int64_t forced_keyint_countdown;                    /* frames until next forced key frame */
    int64_t video_last_pts_read;                        /* Video input last pts read */
    int64_t audio_last_pts_read[MAX_STREAMS];           /* Audio input last pts read */
    int64_t video_last_pts_sent_encode;                 /* Video last pts to encode if tx_type & tx_video */
    int64_t audio_last_pts_sent_encode[MAX_STREAMS];    /* Audio last pts to encode if tx_type & tx_audio */
    int64_t video_last_pts_encoded;                     /* Video last input pts encoded if tx_type & tx_video */
    int64_t audio_last_pts_encoded[MAX_STREAMS];        /* Audio last input pts encoded if tx_type & tx_audio */

    int64_t audio_output_pts;                           /* Used to set PTS directly when using audio FIFO */

    /* Video color metadata reconciled values - used for fixing frame color metadata */
    enum AVColorPrimaries              video_color_primaries;
    enum AVColorTransferCharacteristic video_color_trc;
    enum AVColorSpace                  video_colorspace;
    enum AVColorRange                  video_color_range;

    /* Video filter */
    AVFilterContext *video_buffersink_ctx;
    AVFilterContext *video_buffersrc_ctx;
    AVFilterGraph   *video_filter_graph;

    /* Audio filter */
    AVFilterContext *audio_buffersink_ctx[MAX_STREAMS];
    AVFilterContext *audio_buffersrc_ctx[MAX_STREAMS];
    AVFilterGraph   *audio_filter_graph[MAX_STREAMS];
    int     n_audio_filters;                            /* Number of initialized audio filters */

    int64_t video_frames_written;                       /* Total video frames written so far */
    int64_t audio_frames_written[MAX_STREAMS];          /* Total audio frames written so far */
    int64_t video_pts;                                  /* Video decoder/encoder pts */
    int64_t audio_pts[MAX_STREAMS];                     /* Audio decoder/encoder pts for each track/stream */
    int64_t video_input_start_pts;                      /* In case video input stream starts at PTS > 0 */
    int     video_input_start_pts_notified;             /* Will be set as soon as out_stat_decoding_video_start_pts is fired */
    int64_t audio_input_start_pts[MAX_STREAMS];         /* In case audio input stream starts at PTS > 0 */
    int     audio_input_start_pts_notified;             /* Will be set as soon as out_stat_decoding_audio_start_pts is fired */
    int64_t first_decoding_video_pts;                   /* PTS of first video frame read from the decoder */
    int64_t first_decoding_audio_pts[MAX_STREAMS];      /* PTS of first audio frame read from the decoder */
    int64_t first_encoding_video_pts;                   /* PTS of first video frame sent to the encoder */
    int64_t first_encoding_audio_pts[MAX_STREAMS];      /* PTS of first audio frame sent to the encoder */
    int64_t first_read_packet_pts[MAX_STREAMS];         /* PTS of first packet read - which might not be decodable */

    int64_t video_encoder_prev_pts;     /* Previous pts for video output (encoder) */
    int64_t video_duration;             /* Duration/pts of original frame */
    int64_t audio_duration;             /* Audio duration/pts of original frame when tx_type == tx_all */
    int64_t first_key_frame_pts;        /* First video key frame pts, used to synchronize audio and video in UDP live streams */
    int     pts_residue;                /* Residue of pts lost in output */

    avp_live_proto_t live_proto;        /* Live source protocol: MPEGTS, RTMP, SRT, RTP */
    avp_container_t  live_container;    /* Supported live source containers MPEGTS and FLV */

    int     is_av_synced;               /* will be set to 1 if audio and video are synced */
    int     frame_duration;             /* Will be > 0 if parameter set_equal_fduration is set and doing mez making */
    int     calculated_frame_duration;  /* Approximate/real frame duration of video stream, will be used to fill video frames */

    volatile int    cancelled;
    volatile int    stopped;
} coderctx_t;

/** @brief Content protection / encryption scheme for the output. */
typedef enum crypt_scheme_t {
    crypt_none,     ///< No encryption.
    crypt_aes128,   ///< HLS AES-128 (whole-segment).
    crypt_cenc,     ///< MPEG Common Encryption, AES-CTR (cenc).
    crypt_cbc1,     ///< MPEG Common Encryption, AES-CBC (cbc1).
    crypt_cens,     ///< MPEG Common Encryption, AES-CTR subsample pattern (cens).
    crypt_cbcs      ///< MPEG Common Encryption, AES-CBC subsample pattern (cbcs).
} crypt_scheme_t;

/** @brief Kind of transcoding to perform. Some values are bit flags combined
 *         with @ref xc_video / @ref xc_audio. */
typedef enum xc_type_t {
    xc_none                 = 0,    ///< Unset; treated as @ref xc_all when stream_id < 0.
    xc_video                = 1,    ///< Transcode video.
    xc_audio                = 2,    ///< Transcode audio.
    xc_all                  = 3,    ///< Transcode video and audio (xc_video | xc_audio).
    xc_audio_merge          = 6,    ///< Merge audio streams (0x04 | xc_audio).
    xc_audio_join           = 10,   ///< Join audio streams (0x08 | xc_audio).
    xc_audio_pan            = 18,   ///< Pan audio channels (0x10 | xc_audio).
    xc_mux                  = 32,   ///< Mux pre-made ABR segments into fMP4/MP4.
    xc_extract_images       = 65,   ///< Extract images at given timestamps (0x40 | xc_video).
    xc_extract_all_images   = 129,  ///< Extract all images (0x80 | xc_video).
    xc_probe                = 256   ///< Probe the input only.
} xc_type_t;

/** @brief Watermark overlay image type (handled in get_overlay_filter_string). */
typedef enum image_type {
    unknown_image,  ///< Unknown / unset.
    png_image,      ///< PNG.
    jpg_image,      ///< JPEG.
    gif_image       ///< GIF.
} image_type;

/** @brief Deinterlacing filter selection. */
typedef enum dif_type {
    dif_none        = 0, ///< No deinterlacing.
    dif_bwdif       = 1, ///< bwdif mode 'send_field' (two frames per input frame).
    dif_bwdif_frame = 2  ///< bwdif mode 'send_frame' (one frame per input frame).
} dif_type;

/** @brief Video layout. Values align with ISO/IEC 23001-8 (CICP). */
typedef enum video_layout_t {
    video_layout_mono   = 0,  ///< Monoscopic.
    video_layout_sbs    = 3,  ///< Stereoscopic side-by-side.
    video_layout_tb     = 4,  ///< Stereoscopic top-bottom.
    video_layout_mvhevc = 10  ///< Multi-layer HEVC (MV-HEVC).
} video_layout_t;

#define DRAW_TEXT_SHADOW_OFFSET     0.075
#define MAX_EXTRACT_IMAGES_SZ       100

/**
 * @brief Transcoding parameters for avpipe_init() / the avpipe transcoding API.
 *
 * @invariant Setting video_bitrate (> 0) raises rc_max_rate and rc_buffer_size
 *            to at least video_bitrate (in check_params). rc_max_rate and
 *            rc_buffer_size map to ffmpeg's maxrate and bufsize.
 *
 * @note Each field's @b Default is the effective library behavior when the field
 *       is left unset. Where the library passes a value through unchanged, the
 *       listed default is that of the reference CLI (exc/elv_xc.c); other
 *       bindings (e.g. the Go XcParams / NewXcParams) may set different defaults.
 */
typedef struct xcparams_t {
    /**
     * @brief URL of the input to transcode.
     * @obligation Mandatory
     * @domain a file path, or a network URL (e.g. udp://127.0.0.1:PORT, rtmp://...)
     */
    char    *url;
    /**
     * @brief Copy input packets to the output without transcoding.
     * @obligation Optional
     * @default 0 (transcode)
     * @domain 0 (transcode) or 1 (copy only)
     * @note Forced to 1 for MV-HEVC and Dolby Atmos inputs.
     */
    int     bypass_transcoding;
    /**
     * @brief Output format / packaging.
     * @obligation Mandatory
     * @domain dash, hls, image2, mp4, fmp4, segment, fmp4-segment
     */
    char    *format;
    /**
     * @brief Skip input up to this time before transcoding.
     * @obligation Optional
     * @default 0 (start at the beginning)
     * @domain input-stream PTS ticks; > 0 seeks/skips
     */
    int64_t start_time_ts;
    /**
     * @brief Starting PTS for the output.
     * @obligation Optional
     * @default 0
     * @domain >= 0, output PTS ticks (a negative value is rejected)
     */
    int64_t start_pts;
    /**
     * @brief Length of input to transcode, measured from start_time_ts.
     * @obligation Optional
     * @default -1 (entire source from start_time_ts)
     * @domain input-stream PTS ticks; > 0 bounds the output, or -1
     */
    int64_t duration_ts;
    /**
     * @brief Index of the first output segment.
     * @obligation Optional
     * @default "1" (CLI reference; parsed via atoi)
     * @domain numeric string, segment index >= 1
     */
    char    *start_segment_str;
    /**
     * @brief Output video bitrate.
     * @obligation Optional
     * @default unused — encode by CRF instead (CLI reference: -1)
     * @domain integer > 0, bits/sec
     * @warning Mutually exclusive with crf_str. If > 0, raises rc_max_rate and rc_buffer_size to at least this value.
     * @see crf_str
     */
    int     video_bitrate;
    /**
     * @brief Output audio bitrate.
     * @obligation Optional
     * @default 0 (used as-is; CLI reference: 128000)
     * @domain integer > 0, bits/sec
     */
    int     audio_bitrate;
    /**
     * @brief Output audio sample rate.
     * @obligation Optional
     * @default <= 0: derive from the source (CLI reference: -1)
     * @domain Hz; for the aac encoder must be a valid AAC rate (otherwise replaced with the default AAC rate)
     */
    int     sample_rate;
    /**
     * @brief Output audio channel layout.
     * @obligation Optional
     * @default 0 (preserve the source layout)
     * @domain ffmpeg channel-layout mask; the CLI accepts "mono", "stereo", "5.0", "5.1"
     */
    int     channel_layout;
    /**
     * @brief Constant Rate Factor (CRF) video quality.
     * @obligation Optional
     * @default unused unless set (CLI reference: "23")
     * @domain numeric string, 1 (best) .. 52 (worst)
     * @warning Mutually exclusive with video_bitrate.
     * @see video_bitrate
     */
    char    *crf_str;
    /**
     * @brief Encoder preset (speed/efficiency trade-off).
     * @obligation Optional
     * @default unused unless set (CLI reference: "medium")
     * @domain ultrafast, superfast, veryfast, faster, fast, medium, slow, slower, veryslow
     */
    char    *preset;
    /**
     * @brief Maximum encoding bit rate (ffmpeg maxrate).
     * @obligation Optional
     * @default 0 (applied only if > 0)
     * @domain integer > 0, bits/sec
     * @note Auto-raised to video_bitrate when smaller (see the struct invariant).
     */
    int     rc_max_rate;
    /**
     * @brief Rate-control buffer size (ffmpeg bufsize).
     * @obligation Optional
     * @default 0 (applied only if > 0)
     * @domain integer > 0, bits
     * @note Auto-raised to video_bitrate when smaller (see the struct invariant).
     */
    int     rc_buffer_size;
    /**
     * @brief Audio ABR/mez segment duration, in timescale ticks.
     * @obligation Conditional
     * @default <= 0: derived from seg_duration when that is set
     * @domain timescale ticks, > 0
     * @pre Required when transcoding audio and neither seg_duration is set nor format is "mp4".
     * @see seg_duration
     */
    int64_t audio_seg_duration_ts;
    /**
     * @brief Video ABR/mez segment duration, in timescale ticks.
     * @obligation Conditional
     * @default <= 0: derived from seg_duration when that is set
     * @domain timescale ticks, > 0
     * @pre Required when transcoding video (not image extraction) and neither seg_duration is set nor format is "mp4".
     * @see seg_duration
     */
    int64_t video_seg_duration_ts;
    /**
     * @brief Segment duration in seconds (alternative to the *_seg_duration_ts fields).
     * @obligation Conditional
     * @default none (NULL)
     * @domain a positive number as a string, in seconds
     * @pre Required for format "segment".
     */
    char    *seg_duration;
    /**
     * @brief (Unused.)
     * @internal Declared but not read by the library.
     */
    int     seg_duration_fr;
    /**
     * @brief Index of the first fMP4 fragment.
     * @obligation Optional
     * @default 0
     * @domain integer >= 0
     */
    int     start_fragment_index;
    /**
     * @brief Force an IDR key frame every N frames (sets the GOP size).
     * @obligation Optional
     * @default 0 (use the codec default GOP)
     * @domain integer > 0, frames
     */
    int     force_keyint;
    /**
     * @brief Force all frames to have equal duration.
     * @obligation Optional
     * @default 0 (off)
     * @domain 0 or 1
     * @pre Only takes effect for format "fmp4-segment".
     */
    int     force_equal_fduration;
    /**
     * @brief Video encoder name.
     * @obligation Optional
     * @default "libx264" (CLI reference)
     * @domain an ffmpeg encoder, e.g. libx264, libx265, h264_nvenc, hevc_nvenc, h264_videotoolbox, mjpeg
     */
    char    *ecodec;
    /**
     * @brief Audio encoder name (when xc_type includes audio).
     * @obligation Optional
     * @default "aac" (CLI reference)
     * @domain aac, ac3, eac3, mp2, mp3
     * @note May be forced to eac3 for Dolby Atmos.
     */
    char    *ecodec2;
    /**
     * @brief Video decoder name (used if non-empty).
     * @obligation Optional
     * @default none — auto-select (CLI reference: "")
     * @domain h264, h264_cuvid, jpeg2000, hevc
     */
    char    *dcodec;
    /**
     * @brief Audio decoder name (used if non-empty).
     * @obligation Optional
     * @default none — auto-select (CLI reference: "")
     * @domain e.g. aac, ac3, eac3
     */
    char    *dcodec2;
    /**
     * @brief GPU index for hardware transcoding.
     * @obligation Optional
     * @default -1 (no GPU)
     * @domain integer >= 0 (applied only when >= 0)
     */
    int     gpu_index;
    /**
     * @brief Output (encoded) video height in pixels.
     * @obligation Optional
     * @default -1 (use the source height)
     * @domain positive integer (e.g. 2160, 1080, 720), or -1
     */
    int     enc_height;
    /**
     * @brief Output (encoded) video width in pixels.
     * @obligation Optional
     * @default -1 (use the source width)
     * @domain positive integer (e.g. 3840, 1920, 1280), or -1
     */
    int     enc_width;
    /**
     * @brief AES initialization vector (16 bytes, hex).
     * @obligation Optional
     * @default none — passed to the muxer only when set
     * @domain 32 hex characters (16 bytes)
     * @see crypt_scheme
     */
    char    *crypt_iv;
    /**
     * @brief AES content key (16 bytes, hex).
     * @obligation Optional
     * @default none — passed to the muxer only when set
     * @domain 32 hex characters (16 bytes)
     * @see crypt_scheme
     */
    char    *crypt_key;
    /**
     * @brief Key ID (KID): 16-byte UUID, hex-encoded.
     * @obligation Conditional
     * @domain 32 hex characters: ^[0-9a-fA-F]{32}$
     * @pre Required for Common Encryption schemes (crypt_cenc, crypt_cbc1, crypt_cens, crypt_cbcs).
     * @see crypt_scheme
     */
    char    *crypt_kid;
    /**
     * @brief Key URL written into the manifest.
     * @obligation Optional
     * @default none — written only when set
     * @domain a URL string
     * @see crypt_scheme
     */
    char    *crypt_key_url;
    /**
     * @brief Skip (do not decode) packets before start_time_ts.
     * @obligation Optional
     * @default 0 (off)
     * @domain 0 or 1
     * @pre Only takes effect when start_time_ts > 0.
     */
    int     skip_decoding;

<<<<<<< Updated upstream
    crypt_scheme_t  crypt_scheme;   // Content protection / DRM / encryption [Optional, Default: crypt_none]
    xc_type_t       xc_type;        // Default: 0 means transcode 'everything'
    int             copy_mpegts;    // Create a copy of the input stream (only MPEGTS and SRT)
    int         use_preprocessed_input;     // Use custom UDP handler
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
=======
    /**
     * @brief Content protection / encryption scheme.
     * @obligation Optional
     * @default crypt_none (no encryption)
     * @domain see crypt_scheme_t
     */
    crypt_scheme_t  crypt_scheme;
    /**
     * @brief Kind of transcoding to perform.
     * @obligation Optional
     * @default xc_none, treated as xc_all (transcode everything) when stream_id < 0
     * @domain see xc_type_t
     * @warning Incompatible with stream_id >= 0, which selects a single stream and overrides this.
     * @see stream_id
     */
    xc_type_t       xc_type;
    /**
     * @brief Copy the input elementary stream into the output.
     * @obligation Optional
     * @default 0 (off)
     * @domain 0 or 1; MPEGTS/SRT/RTP inputs only
     * @warning Requires format "fmp4-segment".
     */
    int             copy_mpegts;
    /**
     * @brief Use a custom (preprocessed) UDP input handler instead of the ffmpeg demuxer.
     * @obligation Optional
     * @default 0 (off)
     * @domain 0 or 1
     */
    int         use_preprocessed_input;
    /**
     * @brief Whether the input is seekable.
     * @obligation Optional
     * @default 0 (not seekable)
     * @domain 0 or 1
     * @note A non-seekable stream with its moov box at the end causes many reads up to that atom.
     */
    int         seekable;
    /**
     * @brief Listen mode (wait for an incoming connection), for RTMP.
     * @obligation Optional
     * @default 1 (listen)
     * @domain 0 or 1
     * @see connection_timeout
     */
    int         listen;
    /**
     * @brief Watermark text to overlay.
     * @obligation Optional
     * @default none (no watermark)
     * @domain string up to WATERMARK_STRING_SZ-1 characters
     * @note Watermark priority is timecode > text > overlay image.
     */
    char        *watermark_text;
    /**
     * @brief Watermark X location (ffmpeg expression).
     * @obligation Conditional
     * @domain a position expression string, e.g. "W*0.05"
     * @pre Must be non-empty when a text or timecode watermark is set.
     */
    char        *watermark_xloc;
    /**
     * @brief Watermark Y location (ffmpeg expression).
     * @obligation Conditional
     * @domain a position expression string, e.g. "H*0.9"
     * @pre Must be non-empty when a text or timecode watermark is set.
     */
    char        *watermark_yloc;
    /**
     * @brief Watermark font size relative to frame height (font_size = value * height).
     * @obligation Conditional
     * @domain float in [0, 1]
     * @pre Must be in range when a text or timecode watermark is set.
     */
    float       watermark_relative_sz;
    /**
     * @brief Watermark font color.
     * @obligation Conditional
     * @default none (CLI reference: white)
     * @domain a color name or value
     * @pre Must be non-empty when a text or timecode watermark is set.
     */
    char        *watermark_font_color;
    /**
     * @brief Draw a shadow behind the watermark text.
     * @obligation Optional
     * @default 0 (no shadow)
     * @domain 0 or 1
     * @pre When 1, watermark_shadow_color must be non-empty.
     */
    int         watermark_shadow;
    /**
     * @brief Path to an overlay image file.
     * @obligation Optional
     * @default none
     * @domain a file path
     * @note Read by the CLI (exc/elv_xc.c) to populate watermark_overlay; not read by the library.
     */
    char        *overlay_filename;
    /**
     * @brief Overlay image as an in-memory buffer.
     * @obligation Conditional
     * @default none
     * @domain raw image bytes (length in watermark_overlay_len, type in watermark_overlay_type)
     * @pre watermark_overlay_type must not be unknown_image when an overlay is used.
     */
    char        *watermark_overlay;
    /**
     * @brief Overlay image type.
     * @obligation Conditional
     * @default unknown_image
     * @domain see image_type
     * @pre Must be a known type when watermark_overlay is set.
     */
    image_type  watermark_overlay_type;
    /**
     * @brief Length of the watermark_overlay buffer.
     * @internal Companion length field for watermark_overlay.
     */
    int         watermark_overlay_len;
    /**
     * @brief Watermark shadow color.
     * @obligation Conditional
     * @default none (CLI reference: white)
     * @domain a color name or value
     * @pre Required when watermark_shadow is 1.
     */
    char        *watermark_shadow_color;
    /**
     * @brief Burned-in timecode watermark.
     * @obligation Optional
     * @default none
     * @domain a timecode string, e.g. 00:00:00:00
     * @note Highest watermark priority (timecode > text > overlay).
     * @see watermark_timecode_rate
     */
    char        *watermark_timecode;
    /**
     * @brief Frame rate for the timecode watermark.
     * @obligation Conditional
     * @default -1
     * @domain float frames/sec, > 0
     * @pre Required (> 0) when watermark_timecode is set.
     */
    float       watermark_timecode_rate;
>>>>>>> Stashed changes

    /**
     * @brief Input audio stream indices to transcode.
     * @obligation Optional
     * @domain up to MAX_STREAMS entries, each a stream index >= 0; entries must be unique
     * @see n_audio
     */
    int         audio_index[MAX_STREAMS];
    /**
     * @brief Number of valid entries in audio_index.
     * @internal Companion length field for audio_index.
     */
    int         n_audio;
    /**
     * @brief Synchronize audio to the given video stream id.
     * @obligation Optional
     * @default -1 (disabled)
     * @domain a stream id >= 0; MPEGTS/live with format "fmp4-segment" only
     */
    int         sync_audio_to_stream_id;
    /**
     * @brief Encoding bit depth.
     * @obligation Optional
     * @default 8 (the library sets 8 when unset)
     * @domain 8, 10, or 12
     */
    int         bitdepth;
    /**
     * @brief HDR Maximum Content Light Level / FALL.
     * @obligation Optional
     * @default none (skipped if NULL, empty, or "0,0")
     * @domain "maxCLL,maxFALL" e.g. "1514,172"; libx265 only
     */
    char        *max_cll;
    /**
     * @brief HDR mastering display metadata.
     * @obligation Optional
     * @default none (applied if non-empty)
     * @domain an ffmpeg master-display string; libx265 only
     * @pre If set, profile must be "main10".
     * @see profile
     */
    char        *master_display;
    /**
     * @brief Video layout (mono / stereoscopic).
     * @obligation Optional
     * @default video_layout_mono (0)
     * @domain see video_layout_t
     */
    int         video_layout;
    /**
     * @brief Transcode only the single input stream with this id.
     * @obligation Optional
     * @default -1 (all streams)
     * @domain a stream id >= 0
     * @warning Incompatible with xc_type / n_audio; requires a segment duration; overrides xc_type.
     * @see xc_type
     */
    int         stream_id;
    /**
     * @brief ffmpeg filter-graph descriptor (for audio pan/merge/join).
     * @obligation Conditional
     * @default none
     * @domain an ffmpeg filter description string
     * @pre Required for xc_type audio-pan / audio-merge / audio-join.
     * @warning Not validated by the library; a missing descriptor for those modes crashes at runtime.
     */
    char        *filter_descriptor;
    /**
     * @brief Muxing spec (for xc_type xc_mux).
     * @obligation Conditional
     * @default none
     * @domain a newline-delimited muxing spec
     * @pre Used when xc_type is xc_mux.
     */
    char        *mux_spec;
    /**
     * @brief Extract a frame at this interval.
     * @obligation Conditional
     * @default -1 (use the default interval, DEFAULT_FRAME_INTERVAL_S)
     * @domain timescale ticks; < 0 selects the default
     * @warning Mutually exclusive with extract_images_ts.
     * @see extract_images_ts
     */
    int64_t     extract_image_interval_ts;
    /**
     * @brief Extract frames at these explicit timestamps.
     * @obligation Conditional
     * @default none
     * @domain an array of PTS timestamps (count in extract_images_sz)
     * @pre Required when extract_images_sz > 0.
     * @warning Mutually exclusive with extract_image_interval_ts.
     */
    int64_t     *extract_images_ts;
    /**
     * @brief Number of entries in extract_images_ts.
     * @internal Companion length field for extract_images_ts.
     */
    int         extract_images_sz;

    /**
     * @brief Output video time base, as the denominator of 1/value.
     * @obligation Optional
     * @default 0 (use the input/derived time base)
     * @domain integer > 0 (the denominator)
     */
    int         video_time_base;
    /**
     * @brief Fixed output video frame duration, in the video time base.
     * @obligation Conditional
     * @default 0 (derive per frame)
     * @domain ticks in the video time base, > 0
     * @warning Required (> 0) when deinterlace is bwdif.
     * @see deinterlace
     */
    int         video_frame_duration_ts;

    /**
     * @brief Emit very low-level per-frame debug logs.
     * @obligation Optional
     * @default 0 (off)
     * @domain 0 or 1
     */
    int         debug_frame_level;
    /**
     * @brief Connection timeout for live protocols.
     * @obligation Optional
     * @default 0 (no timeout; CLI reference: 10)
     * @domain seconds; applied only when > 0, with listen and a live protocol
     * @see listen
     */
    int         connection_timeout;
    /**
     * @brief Rotate / transpose the video.
     * @obligation Optional
     * @default 0 (no rotation)
     * @domain 0, 90, 180, 270 degrees; 90/270 swap width and height
     * @warning Cannot be combined with deinterlace or a watermark.
     */
    int         rotate;
    /**
     * @brief Encoder profile.
     * @obligation Optional
     * @default none (auto-select)
     * @domain H.264: baseline, main, extended, high, high10, high422, high444; H.265: main, main10
     * @pre HDR output (master_display set) requires "main10".
     */
    char        *profile;
    /**
     * @brief Encoder level.
     * @obligation Optional
     * @default <= 0 (auto-select)
     * @domain one of {9,10,11,12,13,20,21,22,30,31,32,40,41,42,50,51,52,60,61,62}, or <= 0
     */
    int         level;
    /**
     * @brief Deinterlacing filter.
     * @obligation Optional
     * @default dif_none
     * @domain see dif_type
     * @warning Cannot be combined with rotate; bwdif requires video_frame_duration_ts > 0.
     */
    dif_type    deinterlace;
    /**
     * @brief Original (source) timecode, written as output metadata.
     * @obligation Optional
     * @default none
     * @domain a timecode string, e.g. 01:00:00:00
     */
    char        *timecode;
} xcparams_t;

#define MAX_CODEC_NAME  256

typedef struct side_data_display_matrix_t {
    double rotation;    // Original rotation is CCW with values from -180 to 180
    double rotation_cw; // Computed CW rotation with values 0 to 360
} side_data_display_matrix_t;

typedef struct side_data_t {
    side_data_display_matrix_t display_matrix;
} side_data_t;

typedef struct dovi_info_t {
    uint8_t present;                         // 1 if AV_PKT_DATA_DOVI_CONF side data found
    uint8_t dv_version_major;
    uint8_t dv_version_minor;
    uint8_t dv_profile;
    uint8_t dv_level;
    uint8_t rpu_present_flag;
    uint8_t el_present_flag;
    uint8_t bl_present_flag;
    uint8_t dv_bl_signal_compatibility_id;
} dovi_info_t;

typedef struct stream_info_t {
    int         stream_index;       // Stream index in AVFormatContext
    int         stream_id;          // Format-specific stream ID, set by libavformat during decoding
    int         codec_type;         // Audio or Video
    int         codec_id;
    char        codec_name[MAX_CODEC_NAME+1];
    char        codec_tag_string[AV_FOURCC_MAX_STRING_SIZE]; // 4CC string, e.g. "avc1", "ec-3"
    int64_t     duration_ts;
    AVRational  time_base;
    int64_t     nb_frames;
    int64_t     start_time;
    AVRational  avg_frame_rate;
    AVRational  frame_rate;         // Same as r_frame_rate
    int         sample_rate;        // Audio only, samples per second
    int         channels;           // Audio only, number of audio channels
    int         channel_layout;     // Audio channel layout
    int64_t     bit_rate;
    int         has_b_frames;
    int         width, height;       // Video only

    enum AVPixelFormat  pix_fmt;     // Video only

    AVRational          sample_aspect_ratio;
    AVRational          display_aspect_ratio;
    enum AVFieldOrder   field_order;
    int                 profile;
    int                 level;

    char                color_primaries[16];      // e.g. "bt2020", "bt709"
    char                color_transfer[24];       // e.g. "smpte2084" (PQ), "arib-std-b67" (HLG)
    char                color_space[16];          // e.g. "bt2020nc"
    char                color_range[8];           // "tv" (limited) or "pc" (full)

    char                mastering_display[128];   // AV_PKT_DATA_MASTERING_DISPLAY_METADATA
    char                max_cll[32];              // AV_PKT_DATA_CONTENT_LIGHT_LEVEL
    char                stereo3d_type[32];        // AV_PKT_DATA_STEREO3D

    dovi_info_t         dovi;                    // AV_PKT_DATA_DOVI_CONF
    int                 ec3_joc;                 // 1 if Dolby Atmos (JOC); set when codec_context->profile == AV_PROFILE_EAC3_DDP_ATMOS

    side_data_t         side_data;
    AVDictionary        *tags;
} stream_info_t;

typedef struct container_info_t {
    float duration;
    char *format_name;
} container_info_t;

/* The data structure that is filled by avpipe_probe */
typedef struct xcprobe_t {
    container_info_t container_info;
    stream_info_t *stream_info;    // An array of stream_info_t (usually 2)
} xcprobe_t;


/* Context for the source copy operations (MPEGTS) */
typedef struct cp_ctx_t {
    coderctx_t          encoder_ctx;
    pthread_t           thread_id;
    elv_channel_t       *ch;

    // stream_start_pts is necessary information for getting segment splitting to work correctly. We
    // want to preserve the PTS of the output packets when they are written, but segment.c does the
    // splitting assuming that the PTS of the packets starts at 0. By setting the "initial_offset"
    // option, the packets will be offset by that value, but we need to then reduce the PTS of every
    // packet seen by the same amount.
    int64_t stream_start_pts;

} cp_ctx_t;

typedef int (*associate_thread_f)(int32_t handle);

typedef struct xctx_t {
    coderctx_t          decoder_ctx;
    coderctx_t          encoder_ctx;
    xcparams_t          *params;
    int32_t             index;  // index in xc table
    int32_t             handle; // handle for V2 API
    associate_thread_f  associate_thread;
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
    io_mux_ctx_t        *in_mux_ctx;                    // Input muxer context
    coderctx_t          in_muxer_ctx[MAX_STREAMS];      // Video, audio, captions coder input muxer context (one video, multiple audio/caption)
    ioctx_t             *inctx_muxer[MAX_STREAMS];      // Video, audio, captions io muxer context (one video, multiple audio/caption)
    coderctx_t          out_muxer_ctx;                  // Output muxer

    cp_ctx_t            cp_ctx; // Context for source copy operation

    AVPacket            pkt_array[MAX_STREAMS];
    int                 is_pkt_valid[MAX_STREAMS];

    elv_channel_t       *vc;        // Video frame channel
    elv_channel_t       *ac;        // Audio frame channel
    pthread_t           vthread_id;
    pthread_t           athread_id;
    volatile int        stop;
    volatile int        err;        // Return code of transcoding

} xctx_t;

/* Params that are needed to decode/encode a frame in a thread */
typedef struct xc_frame_t {
    AVPacket    *packet;
    AVFrame     *frame;
    AVFrame     *filt_frame;
    int         stream_index;
} xc_frame_t;

/**
 * out_tracker_t is used to keep information useful for providing stat
 * information about a stream.
 *
 * It is kept within the `avpipe_opaque` field of the AVFormatContext. One
 * out_tracker_t is created for each output stream. The `out_tracker_t`'s
 * lifecycle is associated with the format context, and it will be freed in
 * `avpipe_fini`.
 */
typedef struct out_tracker_t {
    struct avpipe_io_handler_t  *out_handlers;
    coderctx_t                  *encoder_ctx;   /* Needed to get access for stats */
    ioctx_t                     *last_outctx;
    int                         seg_index;
    ioctx_t                     *inctx;         /* Points to input context */
    xc_type_t                   xc_type;

    /** Needed to detect type of encoding frame */
    int video_stream_index;
    int audio_stream_index;

    int output_stream_index;
} out_tracker_t;

typedef struct encoding_frame_stats_t {
    int64_t total_frames_written;   /* Total frames encoded in the xc session */
    int64_t frames_written;         /* Frames encoded in the current segment */
} encoding_frame_stats_t;

/**
 * @brief   Allocates and initializes a xctx_t (transcoder context) for pipelining the input stream.
 *          in_handlers, out_handlers, and params ownership is always on the caller, and will never
 *          be freed or modified by this function. In case of failure xctx is NULL.
 *
 * @param   xctx            Pointer that will be filled with a partially initialized transcoding context.
 * @param   in_handlers     A pointer to input handlers. Must be properly set up by the application.
 * @param   out_handlers    A pointer to output handlers. Must be properly set up by the application.
 * @param   params          A pointer to the parameters for transcoding.
 *
 * @return  Returns 0 if the initialization of an avpipe xctx_t is successful, otherwise returns corresponding eav error.
 */
int
avpipe_init(
    xctx_t **xctx,
    avpipe_io_handler_t *in_handlers,
    avpipe_io_handler_t *out_handlers,
    xcparams_t *params);

/**
 * @brief   Frees the memory and other resources allocated by ffmpeg.
 *
 * @param   xctx       A pointer to the trascoding context that would be destructed.
 * @return  Returns 0.
 */
int
avpipe_fini(
    xctx_t **xctx);

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
 * @param   params          A pointer to the parameters for transcoding/probing.
 * @param   xcprobe         A pointer to the xcprobe_t that could contain probing info.
 * @param   n_streams       Will contail number of streams that are probed if successful.
 * @return  Returns 0 if successful, otherwise corresponding eav error.
 */
int
avpipe_probe(
    avpipe_io_handler_t *in_handlers,
    xcparams_t *params,
    xcprobe_t **xcprobe,
    int *n_streams);

/**
 * @brief   Free all memory allocated by avpipe_probe
 *
 * @param   xcprobe         A pointer to the xcprobe_t containing probing info.
 * @param   n_streams       Number of streams in xcprobe.
 * @return  Returns 0 if successful, otherwise corresponding eav error.
 */
int
avpipe_probe_free(
    xcprobe_t *xcprobe,
    int n_streams);

/**
 * @brief   Starts transcoding. Multiple transcoding operations on the same transcoding context is UB.
 *          In case of failure avpipe_fini() should be called to avoid resource leak.
 *
 * @param   xctx                A pointer to transcoding context.
 * @param   do_intrument        If 0 there will be no instrumentation, otherwise it does some instrumentation
 *                              for some ffmpeg functions.
 * @return  Returns 0 if transcoding is successful, otherwise -1.
 */
int
avpipe_xc(
    xctx_t *xctx,
    int do_instrument);

/**
 * @brief   Initializes the avpipe muxer.
 *
 * @param   xctx            A pointer to a transcoding context.
 * @param   in_handlers     A pointer to input handlers. Must be properly set up by the application.
 * @param   out_handlers    A pointer to output handlers. Must be properly set up by the application.
 * @param   params          A pointer to the parameters for transcoding/muxing.
 * @return  Returns 0 if initializing the muxer is successful, otherwise -1.
 */
int
avpipe_init_muxer(
    xctx_t **xctx,
    avpipe_io_handler_t *in_handlers,
    io_mux_ctx_t *in_mux_ctx,
    avpipe_io_handler_t *out_handlers,
    xcparams_t *params);

/**
 * @brief   Frees the memory and other resources allocated by avpipe muxer/ffmpeg.
 *
 * @param   xctx        A pointer to the trascoding context that would be destructed.
 * @return  Returns 0.
 */
int
avpipe_mux_fini(
    xctx_t **xctx);

/**
 * @brief   Starts avpipe muxer.
 *
 * @param   xctx            A pointer to a transcoding context.
 * @return  Returns 0 if muxing is successful, otherwise -1.
 */
int
avpipe_mux(
    xctx_t *xctx);

/**
 * @brief   Returns avpipe GIT version
 */
char *
avpipe_version();

/**
 * @brief   Allocate memory for extract_images_ts
 * 
 * @param   params  Transcoding parameters
 * @param   size    Array size
 */
void
init_extract_images(
    xcparams_t *params,
    int size);

/**
 * @brief   Helper function avoid dealing with array pointers in Go to set
 *          extract_images_ts
 * 
 * @param   params  Transcoding parameters.
 * @param   index   Array index to set.
 * @param   value   Array value (frame PTS).
 */
void
set_extract_images(
    xcparams_t *params,
    int index,
    int64_t value);

/**
 * @brief   Returns the level based on the input values
 *
 * @param   profile_idc     Profile of the video.
 * @param   bitrate         Bit rate of the video.
 * @param   framerate       Frame rate of the video.
 * @param   width           Width of the video.
 * @param   height          Height of the video.
 *
 * @return  Returns the level.
 */
int
avpipe_h264_guess_level(
    int profile_idc,
    int64_t bitrate,
    int framerate,
    int width,
    int height);

/**
 * @brief   Returns the profile based on the input values
 *
 * @param   bitdepth    Bitdepth of the video.
 * @param   width       Width of the video.
 * @param   height      Height of the video.
 *
 * @return  Returns the profile.
 */
int
avpipe_h264_guess_profile(
    int bitdepth,
    int width,
    int height);

/**
 * @brief   Helper function to obtain FFmpeg constant for an h264 profile name. 
 * 
 * @param   profile_name  A pointer to the profile name.
 * @return  Returns the FFmpeg constant if profile name is valid.
 *          Returns 0 if profile name is NULL. For invalid profile name return -1.
 */
int
avpipe_h264_profile(
    char *profile_name);

/**
 * @brief   Helper function to check level. 
 * 
 * @param   level
 * @return  Returns 1 if the level is valid, otherwise return -1 for an invalid level.
 */
int
avpipe_check_level(
    int level);

/**
 * @brief   Helper function to deep copy an xc_params. In the case of OOM, may fail to initialize
 *          all fields.
 * 
 * @param   p  A pointer to the transcoding parameters to copy.
 */
xcparams_t *
avpipe_copy_xcparams(
    xcparams_t *p);

#endif
