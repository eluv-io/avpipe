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
    int fd;
    int sz;

    /* Read counters */
    int64_t read_bytes;
    int64_t read_pos;
} ioctx_t;

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
typedef struct buf_writer_t_ {
    struct out_handler_t_ *out_handler;
    int fd;

    unsigned char* buf;
    int bufsz;

    int rpos;
    int wpos;

    int bytes_read;
    int bytes_written;
} buf_writer_t;

typedef struct out_handler_t_ {
    buf_writer_t *last_buf_writer;
    int chunk_idx;
} out_handler_t;

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
    char *in_filename,
    char *out_filename,
    txparams_t *params);

int
tx_fini(
    txctx_t **txctx);

int
tx(
    txctx_t *txctx,
    int do_instrument);

