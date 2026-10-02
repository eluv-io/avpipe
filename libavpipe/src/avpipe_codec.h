/*
 * avpipe_codec.h
 *
 * Codec-specific helpers.
 */

#ifndef AVPIPE_CODEC_H
#define AVPIPE_CODEC_H

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/mastering_display_metadata.h>

#include "avpipe_xc.h"

#define HDR10_MASTER_DISPLAY_SIZE 128
#define HDR10_MAX_CLL_SIZE         32

typedef enum hdr10_metadata_source {
    hdr10_metadata_none = 0,
    hdr10_metadata_input,
    hdr10_metadata_xcparams,
    hdr10_metadata_disabled
} hdr10_metadata_source_t;

typedef struct hdr10_metadata {
    char master_display[HDR10_MASTER_DISPLAY_SIZE];
    char max_cll[HDR10_MAX_CLL_SIZE];
    hdr10_metadata_source_t master_display_source;
    hdr10_metadata_source_t max_cll_source;
    int enabled;
} hdr10_metadata_t;

/*
 * Parse x265 master-display string:
 * "G(g_x,g_y)B(b_x,b_y)R(r_x,r_y)WP(wp_x,wp_y)L(l_max,l_min)"
 * (usual chromaticities scaled by 50000, luminance by 10000)
 *
 * Returns eav_success and fills *out on success, or eav_param on parse failure
 */
int
parse_master_display(
    const char *s,
    AVMasteringDisplayMetadata *out);

int
master_display_metadata_valid(
    const AVMasteringDisplayMetadata *metadata);

/*
 * Format AVMasteringDisplayMetadata struct into x265 master-display string
 * Buffer size 128 bytes is sufficient.
 *
 * Returns eav_success on success, or eav_param if either pointer is NULL.
 */
int
format_master_display(
    char *buf,
    size_t buf_size,
    const AVMasteringDisplayMetadata *m);

/*
 * Parse "<MaxCLL>,<MaxFALL>" string.
 *
 * Returns eav_success and fills *out on success, or eav_param on parse failure
 */
int
parse_max_cll(
    const char *s,
    AVContentLightMetadata *out);

int
content_light_metadata_valid(
    const AVContentLightMetadata *metadata);

/*
 * Format AVContentLightMetadata struct into string "<MaxCLL>,<MaxFALL>"
 * Buffer size 32 bytes is sufficient
 *
 * Returns eav_success on success, or eav_param if either pointer is NULL.
 */
int
format_max_cll(
    char *buf,
    size_t buf_size,
    const AVContentLightMetadata *c);

int
attach_master_display(
    AVCodecContext *ctx,
    const char *s);

int
attach_max_cll(
    AVCodecContext *ctx,
    const char *s);

int
attach_master_display_nvenc(
    AVCodecContext *ctx,
    const char *s);

int
attach_max_cll_nvenc(
    AVCodecContext *ctx,
    const char *s);

/* Check if Dolby Vision stream has HDR10 compatibility base layer. */
int
is_dovi_hdr10_compatible(
    const AVStream *stream);

const char *
hdr10_metadata_source_name(
    hdr10_metadata_source_t source);

/* Resolve HDR10 metadata - explicit xcparams override stream metadata. */
int
resolve_hdr10_metadata(
    const AVStream *input_stream,
    const AVCodecContext *decoder_codec_context,
    const xcparams_t *params,
    hdr10_metadata_t *metadata);

/* Apply common Main10 and color metadata (before avcodec_open2). */
int
configure_hdr10_encoder_context(
    AVCodecContext *encoder_codec_context,
    xcparams_t *params,
    const hdr10_metadata_t *metadata);

#endif /* AVPIPE_CODEC_H */
