
#pragma once

#include "avpipe_xc.h"

#define MIN(a,b) (((a)<(b))?(a):(b))
#define MAX(a,b) (((a)>(b))?(a):(b))

void
dump_frame(
    char *msg,
    int num,
    AVFrame *frame,
    int debug_frame_level,
    int tx_type);

void
dump_packet(
    const char *msg,
    AVPacket *p,
    int debug_frame_level,
    int tx_type);

void
dump_decoder(
    coderctx_t *d);

void
dump_encoder(
    coderctx_t *d,
    txparams_t *params);

void
dump_codec_context(
    AVCodecContext *cc);

void
dump_codec_parameters(
    AVCodecParameters *cp);

void
dump_stream(
    AVStream *s);

void
save_gray_frame(
    unsigned char *buf,
    int wrap,
    int xsize,
    int ysize,
    char *name,
    int number);

void
dump_coders(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context);

void
connect_ffmpeg_log();
