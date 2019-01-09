/*
 * elv_xc.h
 */

#pragma once

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavutil/opt.h>

typedef struct ioctx_t {
    /* Application specific IO context */
    void *opaque;

    /* fd is used when doing file IO on the stream */
    int fd;
    unsigned char* buf;
    int bufsz;

    /* Size of input, should be set in in_handler-> avpipe_opener_f() */
    int sz;

    /* Read/write counters, used by input/output handlers */
    int64_t read_bytes;
    int64_t read_pos;
    int64_t write_bytes;
    int64_t write_pos;

    /* Output handlers specific data */
    int stream_index;
} ioctx_t;

typedef int
(*avpipe_opener_f)(
    char *filename,
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
    avpipe_reader_f avpipe_reader;
    avpipe_writer_f avpipe_writer;
    avpipe_seeker_f avpipe_seeker;
} avpipe_io_handler_t;

/* Decoder/encoder context, keeps both video and audio stream ffmpeg contexts */
typedef struct coderctx_t {
    char *file_name;
    AVFormatContext *format_context;

    AVCodec *codec[2];
    AVStream *stream[2];
    AVCodecParameters *codec_parameters[2];
    AVCodecContext *codec_context[2];
    int video_stream_index;
    int audio_stream_index;

    int64_t last_dts;

    /* Filter graph, valid for decoder */
    AVFilterContext *buffersink_ctx;
    AVFilterContext *buffersrc_ctx;
    AVFilterGraph *filter_graph;

    int pts;        /* Decoder/encoder pts */
} coderctx_t;

typedef struct txparams_t {
    int start_time_ts;
    int duration_ts;
    char *start_segment_str;
    int video_bitrate;
    int audio_bitrate;
    int sample_rate;                // Audio sampling rate
    char *crf_str;
    int seg_duration_ts;
    int seg_duration_fr;
    char *seg_duration_secs_str;
    char *codec;
    int enc_height;
    int enc_width;
} txparams_t;

typedef struct txctx_t {
    coderctx_t decoder_ctx;
    coderctx_t encoder_ctx;
    txparams_t *params;
} txctx_t;

/*
 * Implements AVIOContext interface for writing
 */
typedef struct out_tracker_t {
    avpipe_io_handler_t *out_handlers;
    ioctx_t *last_outctx;
    int chunk_idx;
} out_tracker_t;

int
init_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context);

int
elv_io_open(
    struct AVFormatContext *s,
    AVIOContext **pb,
    const char *url,
    int flags,
    AVDictionary **options);

void
elv_io_close(
    struct AVFormatContext *s,
    AVIOContext *pb);

int
tx_init(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    avpipe_io_handler_t *out_handlers,
    char *out_filename,
    txparams_t *params);

int
tx_fini(
    txctx_t **txctx);

int
tx(
    txctx_t *txctx,
    int do_instrument);

