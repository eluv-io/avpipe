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

typedef enum avpipe_buftype_t {
    avpipe_input_stream,
    avpipe_init_stream,     // init_stream
    avpipe_manifest,        // dash.mpd
    avpipe_segment          // chunk-stream
} avpipe_buftype_t;

typedef struct ioctx_t {
    /* Application specific IO context */
    void *opaque;

    avpipe_buftype_t type;
    unsigned char* buf;
    int bufsz;

    /* Size of input, should be set in in_handler-> avpipe_opener_f() */
    int sz;

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

typedef struct out_tracker_t {
    struct avpipe_io_handler_t *out_handlers;
    ioctx_t *last_outctx;
    int seg_index;
    ioctx_t *inctx;     // Points to input context
} out_tracker_t;

/**
 * @brief   Allocates and initializes a txctx_t (transcoder context) for piplining the input stream.
 *
 * @param   txctx           Points to allocated and initialized memory (different fields are initialized by ffmpeg).
 * @param   in_handlers     A pointer to input handlers. Must be properly set up by the application.
 * @param   inctx           A pointer to ioctx_t for input stream. This has to be allocated and initialized
 *                          by the application before calling this function.
 * @param   out_handlers    A pointer to output handlers. Must be properly set up by the application.
 * @param   params          A pointer to the parameters for transcoding.
 *
 * @return  Returns 0 if the initialization of an avpipe txctx_t is successful, otherwise returns -1 on error.
 */
int
avpipe_init(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    avpipe_io_handler_t *out_handlers,
    txparams_t *params);

/**
 * @brief   Frees the memory and other resources allocated by ffmpeg.
 *
 * @param   txctx       A pointer to the trascoding context that would be destructed.
 * @return  Returns 0.
 */
int
avpipe_fini(
    txctx_t **txctx);

/**
 * @brief   Starts transcoding.
 *
 * @param   txctx           A pointer to transcoding context.
 * @param   do_intrument    If 0 there will be no instrumentation, otherwise it does some instrumentation
 *                          for some ffmpeg functions.
 * @return  Returns 0 if transcoding is successful, otherwise -1.
 */
int
avpipe_tx(
    txctx_t *txctx,
    int do_instrument);
#endif
