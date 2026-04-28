/*
 * avpipe_codec.c
 *
 * Codec-specific helpers, factored out of avpipe_xc.c.
 */

#include <stdio.h>
#include <string.h>

#include "avpipe_codec.h"
#include "avpipe_xc.h"
#include "elv_log.h"

/* HEVC SEI mastering_display_colour_volume scaling. */
#define MD_CHROMA_SCALE  50000
#define MD_LUMA_SCALE    10000

int
parse_master_display(
    const char *s,
    AVMasteringDisplayMetadata *out)
{
    int g_x, g_y, b_x, b_y, r_x, r_y, wp_x, wp_y, l_max, l_min;

    if (s == NULL || out == NULL)
        return eav_param;

    /* x265 master-display format. AVRational denominators match HEVC SEI scaling */
    if (sscanf(s, "G(%d,%d)B(%d,%d)R(%d,%d)WP(%d,%d)L(%d,%d)",
               &g_x, &g_y, &b_x, &b_y, &r_x, &r_y, &wp_x, &wp_y, &l_max, &l_min) != 10) {
        elv_err("parse_master_display: bad format, s=\"%s\"", s);
        return eav_param;
    }

    /* AVMasteringDisplayMetadata uses R, G, B order in display_primaries[]. */
    out->display_primaries[0][0] = (AVRational){r_x, MD_CHROMA_SCALE};
    out->display_primaries[0][1] = (AVRational){r_y, MD_CHROMA_SCALE};
    out->display_primaries[1][0] = (AVRational){g_x, MD_CHROMA_SCALE};
    out->display_primaries[1][1] = (AVRational){g_y, MD_CHROMA_SCALE};
    out->display_primaries[2][0] = (AVRational){b_x, MD_CHROMA_SCALE};
    out->display_primaries[2][1] = (AVRational){b_y, MD_CHROMA_SCALE};
    out->white_point[0]          = (AVRational){wp_x, MD_CHROMA_SCALE};
    out->white_point[1]          = (AVRational){wp_y, MD_CHROMA_SCALE};
    out->min_luminance           = (AVRational){l_min, MD_LUMA_SCALE};
    out->max_luminance           = (AVRational){l_max, MD_LUMA_SCALE};
    out->has_primaries = 1;
    out->has_luminance = 1;

    return eav_success;
}

int
format_master_display(
    char *buf,
    size_t buf_size,
    const AVMasteringDisplayMetadata *m)
{
    if (buf == NULL || m == NULL)
        return eav_param;

    /* Inverse of parse_master_display (including scaling) */
    snprintf(buf, buf_size,
        "G(%lld,%lld)B(%lld,%lld)R(%lld,%lld)WP(%lld,%lld)L(%lld,%lld)",
        (long long)av_rescale(m->display_primaries[1][0].num, MD_CHROMA_SCALE, m->display_primaries[1][0].den),
        (long long)av_rescale(m->display_primaries[1][1].num, MD_CHROMA_SCALE, m->display_primaries[1][1].den),
        (long long)av_rescale(m->display_primaries[2][0].num, MD_CHROMA_SCALE, m->display_primaries[2][0].den),
        (long long)av_rescale(m->display_primaries[2][1].num, MD_CHROMA_SCALE, m->display_primaries[2][1].den),
        (long long)av_rescale(m->display_primaries[0][0].num, MD_CHROMA_SCALE, m->display_primaries[0][0].den),
        (long long)av_rescale(m->display_primaries[0][1].num, MD_CHROMA_SCALE, m->display_primaries[0][1].den),
        (long long)av_rescale(m->white_point[0].num,           MD_CHROMA_SCALE, m->white_point[0].den),
        (long long)av_rescale(m->white_point[1].num,           MD_CHROMA_SCALE, m->white_point[1].den),
        (long long)av_rescale(m->max_luminance.num,            MD_LUMA_SCALE,   m->max_luminance.den),
        (long long)av_rescale(m->min_luminance.num,            MD_LUMA_SCALE,   m->min_luminance.den));

    return eav_success;
}

int
parse_max_cll(
    const char *s,
    AVContentLightMetadata *out)
{
    unsigned int max_cll, max_fall;

    if (s == NULL || out == NULL)
        return eav_param;

    if (sscanf(s, "%u,%u", &max_cll, &max_fall) != 2) {
        elv_err("parse_max_cll: bad format, s=\"%s\"", s);
        return eav_param;
    }

    out->MaxCLL  = max_cll;
    out->MaxFALL = max_fall;

    return eav_success;
}

int
format_max_cll(
    char *buf,
    size_t buf_size,
    const AVContentLightMetadata *c)
{
    if (buf == NULL || c == NULL)
        return eav_param;

    snprintf(buf, buf_size, "%u,%u", c->MaxCLL, c->MaxFALL);
    return eav_success;
}

int
attach_master_display(
    AVCodecContext *ctx,
    const char *s)
{
    AVMasteringDisplayMetadata m;
    int rc = parse_master_display(s, &m);
    if (rc != eav_success)
        return rc;

    AVPacketSideData *sd = av_packet_side_data_new(
        &ctx->coded_side_data,
        &ctx->nb_coded_side_data,
        AV_PKT_DATA_MASTERING_DISPLAY_METADATA,
        sizeof(m), 0);
    if (!sd) {
        elv_err("attach_master_display: side data allocation failed");
        return eav_mem_alloc;
    }
    memcpy(sd->data, &m, sizeof(m));
    return eav_success;
}

int
attach_max_cll(
    AVCodecContext *ctx,
    const char *s)
{
    AVContentLightMetadata c;
    int rc = parse_max_cll(s, &c);
    if (rc != eav_success)
        return rc;

    AVPacketSideData *sd = av_packet_side_data_new(
        &ctx->coded_side_data,
        &ctx->nb_coded_side_data,
        AV_PKT_DATA_CONTENT_LIGHT_LEVEL,
        sizeof(c), 0);
    if (!sd) {
        elv_err("attach_max_cll: side data allocation failed");
        return eav_mem_alloc;
    }
    memcpy(sd->data, &c, sizeof(c));
    return eav_success;
}
