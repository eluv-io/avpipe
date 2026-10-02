/*
 * avpipe_codec.c
 *
 * Codec-specific helpers, factored out of avpipe_xc.c.
 */

#include <stdio.h>
#include <string.h>

#include <libavutil/dovi_meta.h>

#include "avpipe_codec.h"
#include "avpipe_xc.h"
#include "elv_log.h"

/* HEVC SEI mastering_display_colour_volume scaling. */
#define MD_CHROMA_SCALE  50000
#define MD_LUMA_SCALE    10000

static int
chroma_coordinate_valid(
    AVRational value)
{
    return value.den > 0 && value.num >= 0 &&
        av_cmp_q(value, (AVRational){1, 1}) <= 0;
}

int
master_display_metadata_valid(
    const AVMasteringDisplayMetadata *metadata)
{
    int all_chromaticities_zero = 1;

    if (!metadata || !metadata->has_primaries || !metadata->has_luminance)
        return 0;

    for (int color = 0; color < 3; color++) {
        for (int coordinate = 0; coordinate < 2; coordinate++) {
            AVRational value = metadata->display_primaries[color][coordinate];
            if (!chroma_coordinate_valid(value))
                return 0;
            if (value.num != 0)
                all_chromaticities_zero = 0;
        }
    }
    for (int coordinate = 0; coordinate < 2; coordinate++) {
        AVRational value = metadata->white_point[coordinate];
        if (!chroma_coordinate_valid(value))
            return 0;
        if (value.num != 0)
            all_chromaticities_zero = 0;
    }

    if (all_chromaticities_zero ||
        metadata->min_luminance.den <= 0 || metadata->min_luminance.num < 0 ||
        metadata->max_luminance.den <= 0 || metadata->max_luminance.num <= 0 ||
        av_cmp_q(metadata->max_luminance, metadata->min_luminance) <= 0)
        return 0;

    return 1;
}

int
parse_master_display(
    const char *s,
    AVMasteringDisplayMetadata *out)
{
    int g_x, g_y, b_x, b_y, r_x, r_y, wp_x, wp_y, l_max, l_min;

    if (s == NULL || out == NULL)
        return eav_param;

    memset(out, 0, sizeof(*out));
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

    if (!master_display_metadata_valid(out)) {
        elv_err("parse_master_display: values are incomplete or invalid, s=\"%s\"", s);
        return eav_param;
    }

    return eav_success;
}

int
format_master_display(
    char *buf,
    size_t buf_size,
    const AVMasteringDisplayMetadata *m)
{
    int length;

    if (buf == NULL || !buf_size || !master_display_metadata_valid(m))
        return eav_param;

    /* Inverse of parse_master_display (including scaling) */
    length = snprintf(buf, buf_size,
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

    return length >= 0 && (size_t)length < buf_size ? eav_success : eav_param;
}

int
content_light_metadata_valid(
    const AVContentLightMetadata *metadata)
{
    if (!metadata || (metadata->MaxCLL == 0 && metadata->MaxFALL == 0))
        return 0;
    if (metadata->MaxCLL != 0 && metadata->MaxFALL > metadata->MaxCLL)
        return 0;
    return 1;
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
    if (!content_light_metadata_valid(out)) {
        elv_err("parse_max_cll: values are empty or inconsistent, s=\"%s\"", s);
        return eav_param;
    }

    return eav_success;
}

int
format_max_cll(
    char *buf,
    size_t buf_size,
    const AVContentLightMetadata *c)
{
    int length;

    if (buf == NULL || !buf_size || !content_light_metadata_valid(c))
        return eav_param;

    length = snprintf(buf, buf_size, "%u,%u", c->MaxCLL, c->MaxFALL);
    return length >= 0 && (size_t)length < buf_size ? eav_success : eav_param;
}

/*
 * Emit the MDCV atom - parse x265 master-display string and write to coded_side_data.
 * Used by both libx265 and nvenc.
 * Must be called before avcodec_open2().
 *
 * Returns eav_success on success, eav_param if the string can't be parsed,
 * or eav_mem_alloc if side-data allocation fails.
 */
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

/*
 * Emit CLLI atom - parse "<MaxCLL>,<MaxFALL>" and write to coded_side-data.
 * Used by both libx265 and nvenc.
 * Must be called before avcodec_open2().
 */
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

/*
 * nvenc-only attach mastering display metadata as AV_FRAME_DATA_MASTERING_DISPLAY_METADATA
 * on decoded_side_data so nvenc wrapper adds SEI mdcv
 * Must be called before avcodec_open2().
 */
int
attach_master_display_nvenc(
    AVCodecContext *ctx,
    const char *s)
{
    AVMasteringDisplayMetadata m;
    int rc = parse_master_display(s, &m);
    if (rc != eav_success)
        return rc;

    AVFrameSideData *sd = av_frame_side_data_new(
        &ctx->decoded_side_data,
        &ctx->nb_decoded_side_data,
        AV_FRAME_DATA_MASTERING_DISPLAY_METADATA,
        sizeof(m), 0);
    if (!sd) {
        elv_err("attach_master_display_nvenc: side data allocation failed");
        return eav_mem_alloc;
    }
    memcpy(sd->data, &m, sizeof(m));
    return eav_success;
}

/*
 * nvenc-only attach max_cll as AV_FRAME_DATA_CONTENT_LIGHT_LEVEL
 * on decoded_side_data so nvenc wrapper adds SEI clli
 * Must be called before avcodec_open2().
 */
int
attach_max_cll_nvenc(
    AVCodecContext *ctx,
    const char *s)
{
    AVContentLightMetadata c;
    int rc = parse_max_cll(s, &c);
    if (rc != eav_success)
        return rc;

    AVFrameSideData *sd = av_frame_side_data_new(
        &ctx->decoded_side_data,
        &ctx->nb_decoded_side_data,
        AV_FRAME_DATA_CONTENT_LIGHT_LEVEL,
        sizeof(c), 0);
    if (!sd) {
        elv_err("attach_max_cll_nvenc: side data allocation failed");
        return eav_mem_alloc;
    }
    memcpy(sd->data, &c, sizeof(c));
    return eav_success;
}

int
is_dovi_hdr10_compatible(
    const AVStream *stream)
{
    const AVPacketSideData *side_data;
    const AVDOVIDecoderConfigurationRecord *dovi;

    if (!stream || !stream->codecpar)
        return 0;

    side_data = av_packet_side_data_get(stream->codecpar->coded_side_data,
        stream->codecpar->nb_coded_side_data, AV_PKT_DATA_DOVI_CONF);
    if (!side_data || side_data->size < sizeof(*dovi))
        return 0;

    dovi = (const AVDOVIDecoderConfigurationRecord *)side_data->data;
    return dovi->bl_present_flag && dovi->dv_bl_signal_compatibility_id == 1;
}

const char *
hdr10_metadata_source_name(
    hdr10_metadata_source_t source)
{
    switch (source) {
    case hdr10_metadata_input:
        return "input";
    case hdr10_metadata_xcparams:
        return "xcparams";
    case hdr10_metadata_disabled:
        return "disabled";
    case hdr10_metadata_none:
    default:
        return "none";
    }
}

static const AVPacketSideData *
stream_side_data(
    const AVStream *stream,
    enum AVPacketSideDataType type)
{
    if (!stream || !stream->codecpar)
        return NULL;
    return av_packet_side_data_get(stream->codecpar->coded_side_data,
        stream->codecpar->nb_coded_side_data, type);
}

static const AVFrameSideData *
decoder_side_data(
    const AVCodecContext *decoder_codec_context,
    enum AVFrameSideDataType type)
{
    if (!decoder_codec_context)
        return NULL;
    return av_frame_side_data_get(decoder_codec_context->decoded_side_data,
        decoder_codec_context->nb_decoded_side_data, type);
}

static void
resolve_input_master_display(
    const AVStream *input_stream,
    const AVCodecContext *decoder_codec_context,
    const char *url,
    hdr10_metadata_t *metadata)
{
    const AVPacketSideData *packet_side_data = stream_side_data(
        input_stream, AV_PKT_DATA_MASTERING_DISPLAY_METADATA);
    const AVFrameSideData *frame_side_data;

    if (packet_side_data &&
        packet_side_data->size >= sizeof(AVMasteringDisplayMetadata)) {
        if (format_master_display(metadata->master_display,
                sizeof(metadata->master_display),
                (const AVMasteringDisplayMetadata *)packet_side_data->data) == eav_success) {
            metadata->master_display_source = hdr10_metadata_input;
            return;
        }
        elv_warn("Ignoring invalid/all-zero stream mastering display metadata, url=%s", url);
    }

    frame_side_data = decoder_side_data(decoder_codec_context,
        AV_FRAME_DATA_MASTERING_DISPLAY_METADATA);
    if (frame_side_data &&
        frame_side_data->size >= sizeof(AVMasteringDisplayMetadata)) {
        if (format_master_display(metadata->master_display,
                sizeof(metadata->master_display),
                (const AVMasteringDisplayMetadata *)frame_side_data->data) == eav_success) {
            metadata->master_display_source = hdr10_metadata_input;
            return;
        }
        elv_warn("Ignoring invalid/all-zero decoded mastering display metadata, url=%s", url);
    }
}

static void
resolve_input_max_cll(
    const AVStream *input_stream,
    const AVCodecContext *decoder_codec_context,
    const char *url,
    hdr10_metadata_t *metadata)
{
    const AVPacketSideData *packet_side_data = stream_side_data(
        input_stream, AV_PKT_DATA_CONTENT_LIGHT_LEVEL);
    const AVFrameSideData *frame_side_data;

    if (packet_side_data && packet_side_data->size >= sizeof(AVContentLightMetadata)) {
        if (format_max_cll(metadata->max_cll, sizeof(metadata->max_cll),
                (const AVContentLightMetadata *)packet_side_data->data) == eav_success) {
            metadata->max_cll_source = hdr10_metadata_input;
            return;
        }
        elv_warn("Ignoring invalid/empty stream content light metadata, url=%s", url);
    }

    frame_side_data = decoder_side_data(decoder_codec_context,
        AV_FRAME_DATA_CONTENT_LIGHT_LEVEL);
    if (frame_side_data && frame_side_data->size >= sizeof(AVContentLightMetadata)) {
        if (format_max_cll(metadata->max_cll, sizeof(metadata->max_cll),
                (const AVContentLightMetadata *)frame_side_data->data) == eav_success) {
            metadata->max_cll_source = hdr10_metadata_input;
            return;
        }
        elv_warn("Ignoring invalid/empty decoded content light metadata, url=%s", url);
    }
}

int
resolve_hdr10_metadata(
    const AVStream *input_stream,
    const AVCodecContext *decoder_codec_context,
    const xcparams_t *params,
    hdr10_metadata_t *metadata)
{
    AVMasteringDisplayMetadata parsed_master_display;
    AVContentLightMetadata parsed_max_cll;
    AVCodecParameters *source = input_stream ? input_stream->codecpar : NULL;
    const char *url;
    int explicit_metadata;
    int rc;

    if (!params || !metadata)
        return eav_param;

    memset(metadata, 0, sizeof(*metadata));
    url = params->url ? params->url : "";

    if (params->master_display && params->master_display[0] != '\0') {
        rc = parse_master_display(params->master_display, &parsed_master_display);
        if (rc != eav_success)
            return rc;
        rc = format_master_display(metadata->master_display,
            sizeof(metadata->master_display), &parsed_master_display);
        if (rc != eav_success)
            return rc;
        metadata->master_display_source = hdr10_metadata_xcparams;
    } else {
        resolve_input_master_display(input_stream, decoder_codec_context,
            url, metadata);
    }

    /* "0,0" explicitly suppresses CLL metadata, including input CLL. */
    if (params->max_cll && params->max_cll[0] != '\0') {
        if (strcmp(params->max_cll, "0,0") == 0) {
            metadata->max_cll_source = hdr10_metadata_disabled;
        } else {
            rc = parse_max_cll(params->max_cll, &parsed_max_cll);
            if (rc != eav_success)
                return rc;
            rc = format_max_cll(metadata->max_cll, sizeof(metadata->max_cll),
                &parsed_max_cll);
            if (rc != eav_success)
                return rc;
            metadata->max_cll_source = hdr10_metadata_xcparams;
        }
    } else {
        resolve_input_max_cll(input_stream, decoder_codec_context, url, metadata);
    }

    explicit_metadata =
        metadata->master_display_source == hdr10_metadata_xcparams ||
        metadata->max_cll_source == hdr10_metadata_xcparams;

    /* Do not mistake HLG carrying static metadata for HDR10 unless overridden. */
    if (source && source->color_trc == AVCOL_TRC_ARIB_STD_B67 &&
        !explicit_metadata) {
        metadata->enabled = 0;
    } else {
        metadata->enabled = explicit_metadata ||
            (source && source->color_trc == AVCOL_TRC_SMPTE2084) ||
            is_dovi_hdr10_compatible(input_stream) ||
            metadata->master_display[0] != '\0' || metadata->max_cll[0] != '\0';
    }

    return eav_success;
}

int
configure_hdr10_encoder_context(
    AVCodecContext *encoder_codec_context,
    xcparams_t *params,
    const hdr10_metadata_t *metadata)
{
    const char *encoder;
    const char *url;
    int is_nvenc;
    int rc;

    if (!encoder_codec_context || !params || !metadata)
        return eav_param;
    if (!metadata->enabled)
        return eav_success;

    encoder = params->ecodec ? params->ecodec : "";
    url = params->url ? params->url : "";
    is_nvenc = strcmp(encoder, "hevc_nvenc") == 0;

    if (params->profile && params->profile[0] != '\0' &&
        strcmp(params->profile, "main10") != 0) {
        elv_err("HDR10 requires HEVC profile=main10, got profile=%s, encoder=%s, url=%s",
            params->profile, encoder, url);
        return eav_param;
    }
    if (params->bitdepth != 10) {
        elv_log("HDR10 overriding bitdepth=%d with bitdepth=10, encoder=%s, url=%s",
            params->bitdepth, encoder, url);
        params->bitdepth = 10;
    }

    encoder_codec_context->color_range = AVCOL_RANGE_MPEG;
    encoder_codec_context->color_primaries = AVCOL_PRI_BT2020;
    encoder_codec_context->color_trc = AVCOL_TRC_SMPTE2084;
    encoder_codec_context->colorspace = AVCOL_SPC_BT2020_NCL;

    if (metadata->master_display[0] != '\0') {
        rc = attach_master_display(encoder_codec_context, metadata->master_display);
        if (rc != eav_success)
            return rc;
        if (is_nvenc) {
            rc = attach_master_display_nvenc(encoder_codec_context,
                metadata->master_display);
            if (rc != eav_success)
                return rc;
        }
    }
    if (metadata->max_cll[0] != '\0') {
        rc = attach_max_cll(encoder_codec_context, metadata->max_cll);
        if (rc != eav_success)
            return rc;
        if (is_nvenc) {
            rc = attach_max_cll_nvenc(encoder_codec_context, metadata->max_cll);
            if (rc != eav_success)
                return rc;
        }
    }

    elv_log("HEVC HDR10 enabled encoder=%s master_display_source=%s max_cll_source=%s, url=%s",
        encoder,
        hdr10_metadata_source_name(metadata->master_display_source),
        hdr10_metadata_source_name(metadata->max_cll_source), url);
    if (metadata->master_display[0] == '\0')
        elv_warn("HDR10 output has no valid mastering display metadata; omitting MDCV instead of synthesizing values, encoder=%s, url=%s",
            encoder, url);
    if (metadata->max_cll[0] == '\0' &&
        metadata->max_cll_source != hdr10_metadata_disabled)
        elv_warn("HDR10 output has no valid MaxCLL/MaxFALL metadata; omitting CLLI instead of synthesizing values, encoder=%s, url=%s",
            encoder, url);

    return eav_success;
}
