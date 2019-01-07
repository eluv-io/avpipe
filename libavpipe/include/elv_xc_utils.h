
#pragma once

#include "elv_xc_test.h"

void
dbg(const char *fmt, ...);

void
dump_frame(
    char *msg,
    int num,
    AVFrame *frame);

void
dump_packet(
    char *msg,
    AVPacket *p);

void
dump_decoder(
    txctx_t *d);

void
dump_encoder(
    txctx_t *d);

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
dump_stats(
    txctx_t *decoder_context,
    txctx_t *encoder_context);
