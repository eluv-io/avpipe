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
#include <libavutil/opt.h>

#define MAX_STREAMS	16

typedef enum avpipe_buftype_t {
    avpipe_input_stream = 0,
    avpipe_manifest = 1,                // dash.mpd
    avpipe_video_init_stream = 2,       // video init_stream
    avpipe_audio_init_stream = 3,       // audio init_stream
    avpipe_video_segment = 4,           // video chunk-stream
    avpipe_audio_segment = 5,           // audio chunk-stream
    avpipe_master_m3u = 6,              // hls master m3u
    avpipe_video_m3u = 7,               // video m3u
    avpipe_audio_m3u = 8,               // audio m3u
    avpipe_aes_128_key = 9,             // AES key
    avpipe_mp4_stream = 10,             // mp4 stream
    avpipe_fmp4_stream = 11,            // fragmented mp4 stream
    avpipe_mp4_segment = 12,            // segmented mp4 stream
    avpipe_fmp4_segment = 13            // segmented fmp4 stream
} avpipe_buftype_t;

typedef struct ioctx_t {
    /* Application specific IO context */
    void *opaque;

    /* Input filename or url */
    char *url;

    avpipe_buftype_t type;
    unsigned char* buf;
    int bufsz;

    /* Size of input, should be set in in_handler-> avpipe_opener_f() */
    int64_t sz;

    /* Read/write counters, used by input/output handlers */
    int64_t read_bytes;
    int64_t read_pos;
    int64_t written_bytes;
    int64_t write_pos;

    /* Output handlers specific data */
    int stream_index;       // usually video=0 and audio=1
    int seg_index;          // segment index if this ioctx is a segment

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

typedef struct avpipe_io_handler_t {
    avpipe_opener_f avpipe_opener;
    avpipe_closer_f avpipe_closer;
    avpipe_reader_f avpipe_reader;
    avpipe_writer_f avpipe_writer;
    avpipe_seeker_f avpipe_seeker;
} avpipe_io_handler_t;

/* Decoder/encoder context, keeps both video and audio stream ffmpeg contexts */
typedef struct coderctx_t {
    AVFormatContext *format_context;

    AVCodec *codec[MAX_STREAMS];
    AVStream *stream[MAX_STREAMS];
    AVCodecParameters *codec_parameters[MAX_STREAMS];
    AVCodecContext *codec_context[MAX_STREAMS];
    int video_stream_index;
    int audio_stream_index;

    int64_t last_dts;
    int64_t last_key_frame;     /* pts of last key frame */
    int64_t input_last_pts_read;
    int64_t input_last_pts_sent_encode;
    int64_t input_last_pos_sent_encode;
    int64_t input_last_pts_encoded;

    /* Filter graph, valid for decoder */
    AVFilterContext *buffersink_ctx;
    AVFilterContext *buffersrc_ctx;
    AVFilterGraph *filter_graph;

    int64_t pts;              /* Decoder/encoder pts */
    int64_t video_input_start_pts;  /* In case video input stream starts at PTS > 0 */
    int64_t audio_input_start_pts;  /* In case audio input stream starts at PTS > 0 */

    int is_mpegts;          /* set to 1 if input format name is "mpegts" */

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
    tx_none,
    tx_video,
    tx_audio,
    tx_all
} tx_type_t;

typedef struct txparams_t {
    char *format;                   // Output format [Required, Values: dash, hls, mp4, fmp4]
    int64_t start_time_ts;          // Transcode the source starting from this time
    int64_t skip_over_pts;              // Like start_time_ts but expressed in input pts
    int64_t start_pts;              // Starting PTS for output
    int64_t duration_ts;            // Transcode time period [-1 for entire source length from start_time_ts]
    char *start_segment_str;        // Specify index of the first segment  TODO: change type to int
    int video_bitrate;
    int audio_bitrate;
    int sample_rate;                // Audio sampling rate
    char *crf_str;
    int rc_max_rate;                // Rate control - max rate
    int rc_buffer_size;             // Rate control - buffer size
    int64_t seg_duration_ts;        // In ts units. It is used for transcoding and producing dash mp4 files
    char *seg_duration;             // In sec units. It is used for transcoding and producing mp4 segments
    int seg_duration_fr;
    int start_fragment_index;
    int force_keyint;               // Force a key (IDR) frame at this interval
    char *ecodec;                   // Video/audio encoder
    char *dcodec;                   // Video/audio decoder
    int enc_height;
    int enc_width;
    char *crypt_iv;                 // 16-byte AES IV in hex [Optional, Default: Generated]
    char *crypt_key;                // 16-byte AES key in hex [Optional, Default: Generated]
    char *crypt_kid;                // 16-byte UUID in hex [Optional, required for CENC]
    char *crypt_key_url;            // Specify a key URL in the manifest [Optional, Default: key.bin]
    crypt_scheme_t crypt_scheme;    // Content protection / DRM / encryption [Optional, Default: crypt_none]
    tx_type_t tx_type;              // Default: 0 means transcode 'everything'
    int seekable;                   // Default: 0 means not seekable. A non seekable stream with moov box in
                                    //          the end causes a lot of reads up to moov atom.
} txparams_t;

#define MAX_CODEC_NAME  256

typedef struct stream_info_t {
    int codec_type;             // Audio or Video
    int codec_id;
    char codec_name[MAX_CODEC_NAME+1];
    int64_t duration_ts;
    AVRational time_base;
    int64_t nb_frames;
    int64_t start_time;
    AVRational avg_frame_rate;
    AVRational frame_rate;      // Same as r_frame_rate
    int sample_rate;            // Audio only, samples per second
    int channels;               // Audio only, number of audio channels
    int channel_layout;         // Audio channel layout
    int ticks_per_frame;
    int64_t bit_rate;
    int has_b_frames;
    int width, height;              // Video only
    enum AVPixelFormat pix_fmt;     // Video only
    AVRational sample_aspect_ratio;
    AVRational display_aspect_ratio;
    enum AVFieldOrder field_order;
} stream_info_t;

typedef struct container_info_t {
    float duration;
    char *format_name;
} container_info_t;

typedef struct txprobe_t {
    container_info_t container_info;
    stream_info_t *stream_info;    // An array of stream_info_t (usually 2)
} txprobe_t;

typedef struct txctx_t {
    coderctx_t decoder_ctx;
    coderctx_t encoder_ctx;
    txparams_t *params;
} txctx_t;

typedef struct out_tracker_t {
    struct avpipe_io_handler_t *out_handlers;
    ioctx_t *last_outctx;
    int seg_index;
    ioctx_t *inctx;     // Points to input context

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
 * @param   bypass_transcode    If it is != 0 then will bypass initialization needed for transcoding,
 *                          otherwise does initialization for transcoding.
 *
 * @return  Returns 0 if the initialization of an avpipe txctx_t is successful, otherwise returns -1 on error.
 */
int
avpipe_init(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    avpipe_io_handler_t *out_handlers,
    txparams_t *params,
    int bypass_transcode);

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
 * @return  Returns <=0 if probing is failed, otherwise number of streams that are probed.
 */
int
avpipe_probe(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    int seekable,
    txprobe_t **txprobe);

/**
 * @brief   Starts transcoding.
 *          In case of failure avpipe_fini() should be called to avoid resource leak.
 *
 * @param   txctx               A pointer to transcoding context.
 * @param   do_intrument        If 0 there will be no instrumentation, otherwise it does some instrumentation
 *                              for some ffmpeg functions.
 * @param   bypass_transcode    If 0 means do filtering, otherwise bypass it.
 * @return  Returns 0 if transcoding is successful, otherwise -1.
 */
int
avpipe_tx(
    txctx_t *txctx,
    int do_instrument,
    int bypass_transcode,
    int debug_frame_level,
    int64_t *last_input_pts);
#endif
