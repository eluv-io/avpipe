
#pragma once

#include "avpipe_xc.h"

#define MIN(a,b) (((a)<(b))?(a):(b))
#define MAX(a,b) (((a)>(b))?(a):(b))

void
dump_frame(
    int is_audio,
    char *msg,
    int num,
    AVFrame *frame,
    int debug_frame_level);

void
dump_packet(
    int is_audio,
    const char *msg,
    AVPacket *p,
    int debug_frame_level);

void
dump_decoder(
    char *url,
    coderctx_t *d);

void
dump_encoder(
    char *url,
    AVFormatContext *format_context,
    xcparams_t *params);

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
dump_trackers(
    AVFormatContext *encoder_format_context,
    AVFormatContext *decoder_format_context);

void
connect_ffmpeg_log();

const char *
stream_type_str(
    coderctx_t *c,
    int idx);

typedef unsigned char      byte;    // Byte is a char
typedef unsigned short int word16;  // 16-bit word is a short int

unsigned int
checksum(
    byte *addr,
    unsigned int count);

void
hex_encode(
    byte *buf,
    int sz,
    char *str);
