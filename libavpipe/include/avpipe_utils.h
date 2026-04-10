
#pragma once

#include "avpipe_xc.h"

#define MIN(a,b) (((a)<(b))?(a):(b))
#define MAX(a,b) (((a)>(b))?(a):(b))

void
dump_frame(
    int is_audio,
    int stream_index,
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
dump_streams(
    char *url,
    AVFormatContext *fmt_ctx);

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

int64_t
parse_duration(
    const char *duration_str,
    AVRational time_base);

/**
 * @brief   Get the crop left edge x from vertical_data for a given frame.
 *          The vertical_data value represents the center of the crop window.
 *          If frame_idx is larger than the data set, use the last entry.
 *          Returns the left edge, clamped left/right.
 *
 * @param   vertical_data   binary encoded array, 4 bytes per frame
 * @param   data_len        number of entries in vertical_data
 * @param   frame_idx       frame index (0-based)
 * @param   scaled_width    width in pixels to scale the fraction into
 * @param   crop_width      crop window width in pixels
 * @return  Crop left edge x in pixels, clamped to valid range.
 */
int
vertical_data_crop_x(
    uint8_t *vertical_data,
    int data_len,
    int frame_idx,
    int scaled_width,
    int crop_width);
