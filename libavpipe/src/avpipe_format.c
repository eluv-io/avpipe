/*
 * avpipe_format.c
 *
 * Container-specific helpers.
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libavutil/log.h>
#include <libavutil/pixdesc.h>

#include <string.h>

#include "avpipe_utils.h"
#include "avpipe_xc.h"
#include "elv_log.h"

#include <sys/time.h>


avp_live_proto_t
find_live_proto(
    ioctx_t *inctx)
{
    if (!inctx || !inctx->url) {
        elv_err("find_live_proto: invalid input context - no URL");
        return avp_proto_none;
    }
    if (!strncmp(inctx->url, "udp://", 6))
        return avp_proto_mpegts;

    if (!strncmp(inctx->url, "rtmp://", 7))
        return avp_proto_rtmp;

    if (!strncmp(inctx->url, "srt://", 6))
        return avp_proto_srt;

    if (!strncmp(inctx->url, "rtp://", 6))
        return avp_proto_rtp;

    return avp_proto_none;
}

/*
 * Return the live stream container type
 */
avp_container_t
find_live_container(
    coderctx_t *decoder_context)
{
    switch (decoder_context->live_proto) {
        case avp_proto_mpegts:
        case avp_proto_srt:
        case avp_proto_rtp:
            return avp_container_mpegts;
        case avp_proto_rtmp:
            return avp_container_flv;
        default:
            return avp_container_none;
    }
}

/*
 * True if the decoder is for a live stream source.
 */
int
is_live_source(
    coderctx_t *ctx)
{
    switch(ctx->live_proto) {
        case avp_proto_mpegts:
        case avp_proto_rtmp:
        case avp_proto_srt:
        case avp_proto_rtp:
            return 1;
        default:
            return 0;
    }
}

/*
 * True if the decoder is for a UDP-based live stream source.
 */
int
is_live_source_udp(
    coderctx_t *ctx)
{
    switch(ctx->live_proto) {
        case avp_proto_mpegts:
        case avp_proto_srt:
        case avp_proto_rtp:
            return 1;
        default:
            return 0;
    }
}

int
is_live_container_mpegts(
    coderctx_t *ctx)
{
    if (ctx->live_container == avp_container_mpegts)
        return 1;
    return 0;
}

/*
 * True if the avpipe job uses custom input handlers
 * (as opposed to using the built-in input readers of libavformat such as rtp://, srt://, ...)
 */
int
is_custom_input(
    coderctx_t *ctx)
{
    // For filemaking / mux input this may be null
    if (ctx->inctx
        && ctx->inctx->params
        && ctx->inctx->params->use_preprocessed_input) {
        return 1;
    }

    switch (ctx->live_proto) {
        case avp_proto_rtmp:
        case avp_proto_srt:
        case avp_proto_rtp:
            return 0;
        default:
            // Proceed
            break;
    }
    return 1;
}

int
is_bypass_bframes(
    coderctx_t *decoder_context,
    xcparams_t *params,
    int stream_index)
{
    if (!decoder_context || !params ||
        stream_index < 0 ||
        stream_index >= MAX_STREAMS ||
        stream_index != decoder_context->video_stream_index ||
        !params->bypass_transcoding ||
        !params->format ||
        strcmp(params->format, "dash"))
        return 0;

    if (decoder_context->codec_context[stream_index] &&
        decoder_context->codec_context[stream_index]->has_b_frames > 0)
        return 1;

    if (decoder_context->codec_parameters[stream_index] &&
        decoder_context->codec_parameters[stream_index]->video_delay > 0)
        return 1;

    if (decoder_context->stream[stream_index] &&
        decoder_context->stream[stream_index]->codecpar &&
        decoder_context->stream[stream_index]->codecpar->video_delay > 0)
        return 1;

    return 0;
}


int
num_audio_output(
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int n_decoder_audio = decoder_context ? decoder_context->n_audio : 0;
    if (!params)
        return 0;

    if (params->xc_type == xc_audio_merge || params->xc_type == xc_audio_join || params->xc_type == xc_audio_pan)
        return 1;

    return params->n_audio > 0 ? params->n_audio : n_decoder_audio;
}

/*
 * Given a source audio stream index, return the array index in the decoder 'audio_stream_index' array, if selected.
 * This is used to index the 'format_context2' array.
 * Return -1 if this stream index is not selected (was not part of the xc_params audio_index array)
 */
int
selected_decoded_audio(
    coderctx_t *decoder_context,
    int stream_index)
{
    if (decoder_context->n_audio <= 0)
        return -1;

    for (int i=0; i<decoder_context->n_audio; i++) {
        if (decoder_context->audio_stream_index[i] == stream_index)
            return i;
    }

    return -1;
}

/*
 * Return the array index into the encoder 'audio_stream_index' array (which is also used by the encoder 'codec_context' array),
 * from the array index into the decoder 'audio_stream_index' array (as returned by selected_decoded_audio)
 */
int
audio_output_stream_index(
    coderctx_t *decoder_context,
    xcparams_t *params,
    int audio_stream_index)
{
    int output_stream_index;

    if (audio_stream_index < 0)
        return -1;

    output_stream_index = decoder_context->audio_stream_index[audio_stream_index];

    if (params->xc_type == xc_audio_merge ||
        params->xc_type == xc_audio_join ||
        params->xc_type == xc_audio_pan) {
        // Only one audio output
        output_stream_index = 0;
    }
    return output_stream_index;
}

int
get_channel_layout_for_encoder(int channel_layout)
{
    switch (channel_layout) {
    case AV_CH_LAYOUT_2_1:
        channel_layout = AV_CH_LAYOUT_SURROUND;
        break;
    case AV_CH_LAYOUT_2_2:
        channel_layout = AV_CH_LAYOUT_QUAD;
        break;
    case AV_CH_LAYOUT_5POINT0:
        channel_layout = AV_CH_LAYOUT_5POINT0_BACK;
        break;
    case AV_CH_LAYOUT_5POINT1:
        channel_layout = AV_CH_LAYOUT_5POINT1_BACK;
        break;
    case AV_CH_LAYOUT_6POINT0_FRONT:
        channel_layout = AV_CH_LAYOUT_6POINT0;
        break;
    case AV_CH_LAYOUT_6POINT1_BACK:
    case AV_CH_LAYOUT_6POINT1_FRONT:
        channel_layout = AV_CH_LAYOUT_6POINT1;
        break;
    case AV_CH_LAYOUT_7POINT0_FRONT:
        channel_layout = AV_CH_LAYOUT_7POINT0;
        break;
    case AV_CH_LAYOUT_7POINT1_WIDE_BACK:
    case AV_CH_LAYOUT_7POINT1_WIDE:
        channel_layout = AV_CH_LAYOUT_7POINT1;
        break;
    }

    /* If there is no input channel layout, set the default encoder channel layout to stereo */
    if (channel_layout == 0)
        channel_layout = AV_CH_LAYOUT_STEREO;

    return channel_layout;
}

#define TIMEBASE_THRESHOLD  10000

/* Calculate final output timebase based on the codec timebase by replicating
 * the logic in the ffmpeg muxer: multiply by 2 until greater than 10,000
 */
int
calc_timebase(
    xcparams_t *params,
    int is_video,
    int timebase)
{
    if (timebase <= 0) {
        elv_err("calc_timebase invalid timebase=%d", timebase);
        return timebase;
    }

    if (is_video && params->video_time_base > 0)
        timebase = params->video_time_base;

    while (timebase < TIMEBASE_THRESHOLD)
        timebase *= 2;

    return timebase;
}

int
packet_clone(
    AVPacket *src,
    AVPacket **dst
) {
    *dst = av_packet_alloc();
    if (!*dst) {
        return -1;
    }
    if (av_packet_ref(*dst, src) < 0) {
        av_packet_free(dst);
        return -1;
    }
    return 0;
}

/*
 * For both mez part and ABR segment jobs, PTS-based segmentation or cutting is subject to small
 * imperfections in source PTS (for example if source PTS is one or two units shorts, it will be skipped
 * incorrectly, or for segmentation in segment.c we miss the iframe and cut on the following iframe creating a longer segment).
 *
 * Calculate a minimal tolerance specifically for each type of source media to fix concrete problems.
 * If the tolerance is too large we risk to match too early or too late.
 */
int
segmentation_tolerance(
    coderctx_t *decoder_context,
    int stream_index
) {
    int i;
    int tolerance = 0;

    if ((i = selected_decoded_audio(decoder_context, stream_index)) >= 0) {
        if (decoder_context->format_context->streams[i]->codecpar->codec_id == AV_CODEC_ID_AAC) {
            tolerance = 15; /* 1 ts unit per ABR segment to compensate for frame duration 1023 */
        }
    }
    return tolerance;
}

/*
 * Update an audio frame by skipping the necessary number of samples.
 * Update frame nb_samples to the remaining samples in the frame (which may be 0 if the frame is to be skipped entirely)
 * Many audio decoders use the concept of 'priming' and require skipping of a number of samples.
 * Opus specifically sets codec_context->delay to the number of samples to be skipped.
 * In most cases the audio frames to be skipped might have a negative PTS but that is not a rule/standard.
 * Limitation: we expect we only skip samples in the first frame (log warning if not true)
 *
 * PENDING(SS) Currently unused but will be used in encode_frame()
 */
int
audio_skip_samples(
    AVCodecContext *codec_ctx,
    AVFrame *frame) {

    // Only skipping for Opus inputs currently
    if (codec_ctx->codec_id != AV_CODEC_ID_OPUS) {
        return eav_success;
    }

    int samples_to_skip = codec_ctx->delay; // Defined by the Opus format
    if (samples_to_skip <= 0) {
        return eav_success;
    }

    if (samples_to_skip > frame->nb_samples) {
        // Unexpected - we need to skip more than one frame
        elv_warn("Unexpected audio stream delay nb_frames=%d skip_now=%d", frame->nb_samples, samples_to_skip);
        frame->nb_samples = 0;
        return eav_success;
    }

    int sample_size = av_get_bytes_per_sample(codec_ctx->sample_fmt);
    if (sample_size <= 0) {
        // Unknown sample format - this frame is not good
        return eav_receive_frame;
    }

    // Skip samples in either packed (one channel, interleaved) or planar data (multiple channels)
    int skip_bytes = samples_to_skip * sample_size;
    for (int ch = 0; ch < codec_ctx->ch_layout.nb_channels; ch++) {
        if (frame->data[ch]) {
            frame->data[ch] += skip_bytes;
        }
    }

    frame->nb_samples -= samples_to_skip;
    return eav_success;
}

/*
 * Rescale an AVFrame before sending to the filter or encoder.
 * When rescaling a decoded frame before sending it to the filter or encoder, use the
 * decoder codec_context timebase as source and the encoder codec timebase as target
 */
void frame_rescale_time_base(
    AVFrame *frame, AVRational src_time_base, AVRational dst_time_base) {
    if (frame->pts != AV_NOPTS_VALUE)
        frame->pts = av_rescale_q(frame->pts, src_time_base, dst_time_base);

    if (frame->pkt_dts != AV_NOPTS_VALUE)
        frame->pkt_dts = av_rescale_q(frame->pkt_dts, src_time_base, dst_time_base);

    if (frame->duration > 0)
        frame->duration = av_rescale_q(frame->duration, src_time_base, dst_time_base);
}

/*
 * Copy coded side data from the input stream's codecpar to the output stream's codecpar.
 * This is needed in bypass mode to preserve metadata such as stereo 3D info for MV-HEVC.
 *
 * Note: avcodec_parameters_copy() copies codecpar->coded_side_data, so this function
 * only needs to handle any additional side data entries that may not have been copied
 * (e.g., side data added by the demuxer to the AVStream but not to codecpar).
 *
 * Uses the FFmpeg 8.x side data API (av_packet_side_data_new).
 */
int
copy_stream_side_data(
    AVStream *out_stream,
    const AVStream *in_stream)
{
    const AVCodecParameters *in_cp = in_stream->codecpar;
    AVCodecParameters *out_cp = out_stream->codecpar;

    for (int i = 0; i < in_cp->nb_coded_side_data; i++) {
        const AVPacketSideData *sd = &in_cp->coded_side_data[i];

        /* Skip if this type already exists in the output (e.g., copied by avcodec_parameters_copy) */
        if (av_packet_side_data_get(out_cp->coded_side_data,
                                    out_cp->nb_coded_side_data,
                                    sd->type))
            continue;

        AVPacketSideData *new_sd = av_packet_side_data_new(
            &out_cp->coded_side_data,
            &out_cp->nb_coded_side_data,
            sd->type, sd->size, 0);
        if (!new_sd) {
            elv_err("Failed to allocate side data type=%d size=%zu", sd->type, sd->size);
            return -1;
        }
        memcpy(new_sd->data, sd->data, sd->size);
    }

    return 0;
}

/*
 * Detect MV-HEVC (Multiview HEVC) input stream. Two flavors exist:
 *
 * - Apple Spatial Video: Main 10 profile with multilayer extensions.
 *   The MOV demuxer sets AV_DISPOSITION_MULTILAYER when it finds an lhvC box.
 *   Container metadata includes stereo3d/spherical side data (from vexu/eyes boxes).
 *
 * - x265 Multiview: Uses AV_PROFILE_HEVC_MULTIVIEW_MAIN (profile 6) signaled in the VPS.
 *   The HEVC parser does not set AV_DISPOSITION_MULTILAYER from the bitstream,
 *   so the caller must set it on the output stream to ensure the MP4 muxer writes the lhvC atom.
 *
 * Returns 1 if the stream is MV-HEVC, 0 otherwise.
 */
int
is_mvhevc(
    const AVStream *stream)
{
    if (!stream || !stream->codecpar)
        return 0;

    if (stream->codecpar->codec_id != AV_CODEC_ID_HEVC)
        return 0;

    if (stream->disposition & AV_DISPOSITION_MULTILAYER)
        return 1;

    if (stream->codecpar->profile == AV_PROFILE_HEVC_MULTIVIEW_MAIN)
        return 1;

    return 0;
}

/*
 * Detects Dolby Atmos audio streams.
 * Dolby Atmos is carried as JOC (Joint Object Coding) metadata on top of E-AC-3 or TrueHD.
 *
 * Returns 1 if the stream is Dolby Atmos, 0 otherwise.
 */
int
is_dolby_atmos(
    const AVStream *stream)
{
    if (!stream || !stream->codecpar)
        return 0;

    if (stream->codecpar->codec_id == AV_CODEC_ID_EAC3 &&
        stream->codecpar->profile == AV_PROFILE_EAC3_DDP_ATMOS)
        return 1;

    if (stream->codecpar->codec_id == AV_CODEC_ID_TRUEHD &&
        stream->codecpar->profile == AV_PROFILE_TRUEHD_ATMOS)
        return 1;

    return 0;
}

/*
 * Detect Dolby Vision: returns 1 if the stream carries a Dolby Vision
 * configuration, 0 otherwise. Two signals are checked:
 *   1. codec_tag is dvh1 or dvhe — these sample entry types are Dolby Vision
 *      by definition, even without a DOVI side-data record.
 *   2. AV_PKT_DATA_DOVI_CONF side data is present on codecpar — covers
 *      hvc1/hev1+dvvC (Profile 8) streams.
 */
int
is_dovi(
    const AVStream *stream)
{
    if (!stream || !stream->codecpar)
        return 0;
    uint32_t tag = stream->codecpar->codec_tag;
    if (tag == MKTAG('d','v','h','1') || tag == MKTAG('d','v','h','e'))
        return 1;
    return av_packet_side_data_get(stream->codecpar->coded_side_data,
                                   stream->codecpar->nb_coded_side_data,
                                   AV_PKT_DATA_DOVI_CONF) != NULL;
}

/*
 * Verify the source video color metadata matches expected HDR overrides.
 */
void
verify_hdr_source_color(
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int idx = decoder_context->video_stream_index;
    if (idx < 0 || !decoder_context->stream[idx] || !decoder_context->stream[idx]->codecpar)
        return;
    AVCodecParameters *src = decoder_context->stream[idx]->codecpar;
    const char *url = params ? params->url : "";

    if (src->color_primaries != AVCOL_PRI_BT2020) {
        const char *n = av_color_primaries_name(src->color_primaries);
        elv_warn("HDR source color_primaries mismatch: expected bt2020(%d), source=%s(%d), url=%s",
            (int)AVCOL_PRI_BT2020, n ? n : "?", (int)src->color_primaries, url);
    }
    if (src->color_trc != AVCOL_TRC_SMPTE2084) {
        const char *n = av_color_transfer_name(src->color_trc);
        elv_warn("HDR source transfer mismatch: expected smpte2084(%d), source=%s(%d), url=%s",
            (int)AVCOL_TRC_SMPTE2084, n ? n : "?", (int)src->color_trc, url);
    }
    if (src->color_space != AVCOL_SPC_BT2020_NCL) {
        const char *n = av_color_space_name(src->color_space);
        elv_warn("HDR source matrix mismatch: expected bt2020nc(%d), source=%s(%d), url=%s",
            (int)AVCOL_SPC_BT2020_NCL, n ? n : "?", (int)src->color_space, url);
    }
    if (src->color_range != AVCOL_RANGE_MPEG) {
        const char *n = av_color_range_name(src->color_range);
        elv_warn("HDR source range mismatch: expected tv/MPEG(%d), source=%s(%d), url=%s",
            (int)AVCOL_RANGE_MPEG, n ? n : "?", (int)src->color_range, url);
    }
}

/*
 * Copy source color metadata into encoder codec context if specified
 * Must be called before avcodec_open2().
 *
 * Setting both VUI and colr is required for HEVC (HEVC decoder overrides codecpar->color*
 * with the SPS VUI values).
 */
void
copy_source_color_to_output(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context)
{
    int idx = decoder_context->video_stream_index;
    if (idx < 0 || !decoder_context->stream[idx] || !decoder_context->stream[idx]->codecpar)
        return;
    if (!encoder_context->codec_context[idx])
        return;
    AVCodecParameters *src = decoder_context->stream[idx]->codecpar;
    AVCodecContext    *dst = encoder_context->codec_context[idx];
    if (src->color_primaries != AVCOL_PRI_UNSPECIFIED)
        dst->color_primaries = src->color_primaries;
    if (src->color_trc != AVCOL_TRC_UNSPECIFIED)
        dst->color_trc = src->color_trc;
    if (src->color_space != AVCOL_SPC_UNSPECIFIED)
        dst->colorspace = src->color_space;
    if (src->color_range != AVCOL_RANGE_UNSPECIFIED)
        dst->color_range = src->color_range;
}

static int
known_color_primaries(
    enum AVColorPrimaries value)
{
    return value > AVCOL_PRI_RESERVED0 &&
        value != AVCOL_PRI_UNSPECIFIED &&
        value != AVCOL_PRI_RESERVED &&
        value < AVCOL_PRI_NB;
}

static int
known_color_trc(
    enum AVColorTransferCharacteristic value)
{
    return value > AVCOL_TRC_RESERVED0 &&
        value != AVCOL_TRC_UNSPECIFIED &&
        value != AVCOL_TRC_RESERVED &&
        value < AVCOL_TRC_NB;
}

static int
known_color_space(
    enum AVColorSpace value,
    enum AVPixelFormat pix_fmt)
{
    if (value == AVCOL_SPC_UNSPECIFIED || value < 0 || value >= AVCOL_SPC_NB)
        return 0;
    /* AVCOL_SPC_RGB is value 0 (unset) - valid for RGB but not for YUV */
    if (value == AVCOL_SPC_RGB) {
        const AVPixFmtDescriptor *desc = av_pix_fmt_desc_get(pix_fmt);
        if (!desc || !(desc->flags & AV_PIX_FMT_FLAG_RGB))
            return 0;
    }
    return 1;
}

static int
known_color_range(
    enum AVColorRange value)
{
    return value > AVCOL_RANGE_UNSPECIFIED &&
        value < AVCOL_RANGE_NB;
}

/*
 * Resolve video stream color info when preparing the decoder.
 * These values are used for both:
 * - video input filter
 * - fix frame color meta when primaries/trc are decoded as "reserved" (0)
 * Fixes filter error:
 * "Unsupported input (Operation not supported): fmt:yuv422p10le csp:gbr prim:reserved trc:reserved -> fmt:yuv420p csp:bt709 prim:reserved trc:reserved"
 */
void
reconcile_decoder_video_color(
    coderctx_t *decoder_context,
    int stream_index,
    const char *url)
{
    AVCodecContext *codec_context;
    AVCodecParameters *codecpar;
    enum AVPixelFormat pix_fmt;

    decoder_context->video_color_primaries = AVCOL_PRI_UNSPECIFIED;
    decoder_context->video_color_trc       = AVCOL_TRC_UNSPECIFIED;
    decoder_context->video_colorspace      = AVCOL_SPC_UNSPECIFIED;
    decoder_context->video_color_range     = AVCOL_RANGE_UNSPECIFIED;

    if (stream_index < 0 ||
        !decoder_context->codec_context[stream_index] ||
        !decoder_context->stream[stream_index] ||
        !decoder_context->stream[stream_index]->codecpar)
        return;

    codec_context = decoder_context->codec_context[stream_index];
    codecpar = decoder_context->stream[stream_index]->codecpar;
    pix_fmt = codec_context->pix_fmt;

    if (known_color_primaries(codec_context->color_primaries)) {
        decoder_context->video_color_primaries = codec_context->color_primaries;
        if (known_color_primaries(codecpar->color_primaries) &&
            codecpar->color_primaries != codec_context->color_primaries)
            elv_warn("reconcile_decoder_video_color: primaries differ, decoder=%s codecpar=%s, url=%s",
                av_color_primaries_name(codec_context->color_primaries),
                av_color_primaries_name(codecpar->color_primaries), url);
    } else if (known_color_primaries(codecpar->color_primaries)) {
        decoder_context->video_color_primaries = codecpar->color_primaries;
    }

    if (known_color_trc(codec_context->color_trc)) {
        decoder_context->video_color_trc = codec_context->color_trc;
        if (known_color_trc(codecpar->color_trc) &&
            codecpar->color_trc != codec_context->color_trc)
            elv_warn("reconcile_decoder_video_color: transfer differ, decoder=%s codecpar=%s, url=%s",
                av_color_transfer_name(codec_context->color_trc),
                av_color_transfer_name(codecpar->color_trc), url);
    } else if (known_color_trc(codecpar->color_trc)) {
        decoder_context->video_color_trc = codecpar->color_trc;
    }

    if (known_color_space(codec_context->colorspace, pix_fmt)) {
        decoder_context->video_colorspace = codec_context->colorspace;
        if (known_color_space(codecpar->color_space, pix_fmt) &&
            codecpar->color_space != codec_context->colorspace)
            elv_warn("reconcile_decoder_video_color: matrix differ, decoder=%s codecpar=%s, url=%s",
                av_color_space_name(codec_context->colorspace),
                av_color_space_name(codecpar->color_space), url);
    } else if (known_color_space(codecpar->color_space, pix_fmt)) {
        decoder_context->video_colorspace = codecpar->color_space;
    }

    if (known_color_range(codec_context->color_range)) {
        decoder_context->video_color_range = codec_context->color_range;
        if (known_color_range(codecpar->color_range) &&
            codecpar->color_range != codec_context->color_range)
            elv_warn("reconcile_decoder_video_color: range differ, decoder=%s codecpar=%s, url=%s",
                av_color_range_name(codec_context->color_range),
                av_color_range_name(codecpar->color_range), url);
    } else if (known_color_range(codecpar->color_range)) {
        decoder_context->video_color_range = codecpar->color_range;
    }
}

/*
 * Fix frame color metadata for frames that are decoded as "reserved" (0)
 */
void
fix_video_frame_color(
    coderctx_t *decoder_context,
    AVFrame *frame)
{
    if (!frame)
        return;

    if (!known_color_primaries(frame->color_primaries))
        frame->color_primaries = decoder_context->video_color_primaries;
    if (!known_color_trc(frame->color_trc))
        frame->color_trc = decoder_context->video_color_trc;
    if (!known_color_space(frame->colorspace, frame->format))
        frame->colorspace = decoder_context->video_colorspace;
    if (!known_color_range(frame->color_range))
        frame->color_range = decoder_context->video_color_range;
}

/* Broad category for a primaries value: 0=unknown, 1=BT.709, 2=BT.601, 3=HDR */
static int
color_pri_cat(enum AVColorPrimaries p)
{
    switch (p) {
    case AVCOL_PRI_BT709:     return 1;
    case AVCOL_PRI_SMPTE170M:
    case AVCOL_PRI_BT470BG:
    case AVCOL_PRI_BT470M:    return 2;
    case AVCOL_PRI_BT2020:    return 3;
    default:                  return 0;
    }
}

/* Broad category for a TRC value: 0=unknown, 1=BT.709, 2=BT.601, 3=HDR */
static int
color_trc_cat(enum AVColorTransferCharacteristic t)
{
    switch (t) {
    case AVCOL_TRC_BT709:          return 1;
    case AVCOL_TRC_SMPTE170M:
    case AVCOL_TRC_GAMMA22:
    case AVCOL_TRC_GAMMA28:        return 2;
    case AVCOL_TRC_SMPTE2084:
    case AVCOL_TRC_ARIB_STD_B67:   return 3;
    default:                       return 0;
    }
}

/* Broad category for a colorspace value: 0=unknown, 1=BT.709, 2=BT.601, 3=HDR */
static int
color_spc_cat(enum AVColorSpace s)
{
    switch (s) {
    case AVCOL_SPC_BT709:          return 1;
    case AVCOL_SPC_SMPTE170M:
    case AVCOL_SPC_BT470BG:        return 2;
    case AVCOL_SPC_BT2020_NCL:
    case AVCOL_SPC_BT2020_CL:      return 3;
    default:                       return 0;
    }
}

/* For DASH/HLS output, dashenc creates inner MP4 muxer contexts that we cannot
 * inject write_colr into. movenc's colr-box gate requires at least one of
 * primaries/trc/space to be non-UNSPECIFIED; when only color_range is set the
 * box is skipped and the range value is lost.
 *
 * When color metadata is partially or fully absent, fill in the missing fields
 * so the colr box is written and range is preserved.  Detection rules:
 *   - All three UNSPECIFIED → synthesize BT.709 (standard SDR default)
 *   - Partial metadata → detect color space from set fields; fill only the gaps
 *   - Inconsistent metadata (e.g. bt2020 primaries + bt709 trc) → log error, no-op
 *   - HDR without an unambiguous TRC → no-op (can't distinguish HDR10 from HLG)
 */
void
dash_synthesize_color_defaults(
    xcparams_t *params,
    AVCodecParameters *codecpar)
{
    if (strcmp(params->format, "dash") && strcmp(params->format, "hls"))
        return;
    if (!codecpar || codecpar->color_range == AVCOL_RANGE_UNSPECIFIED)
        return;

    enum AVColorPrimaries              pri = codecpar->color_primaries;
    enum AVColorTransferCharacteristic trc = codecpar->color_trc;
    enum AVColorSpace                  spc = codecpar->color_space;

    /* All three already set — nothing to do */
    if (pri != AVCOL_PRI_UNSPECIFIED &&
        trc != AVCOL_TRC_UNSPECIFIED &&
        spc != AVCOL_SPC_UNSPECIFIED)
        return;

    /* Detect overall color category from whichever fields are set */
    int cats[3] = { color_pri_cat(pri), color_trc_cat(trc), color_spc_cat(spc) };
    int cat = 0;
    for (int i = 0; i < 3; i++) {
        if (cats[i] == 0)
            continue;
        if (cat != 0 && cat != cats[i]) {
            elv_err("dash_synthesize_color_defaults: incoherent color metadata "
                    "(primaries=%d trc=%d space=%d), url=%s", pri, trc, spc, params->url);
            return;
        }
        cat = cats[i];
    }

    if (cat == 0) {
        /* All UNSPECIFIED — synthesize BT.709 */
        codecpar->color_primaries = AVCOL_PRI_BT709;
        codecpar->color_trc       = AVCOL_TRC_BT709;
        codecpar->color_space     = AVCOL_SPC_BT709;
        return;
    }

    if (cat == 1) {
        /* BT.709 */
        if (pri == AVCOL_PRI_UNSPECIFIED) codecpar->color_primaries = AVCOL_PRI_BT709;
        if (trc == AVCOL_TRC_UNSPECIFIED) codecpar->color_trc       = AVCOL_TRC_BT709;
        if (spc == AVCOL_SPC_UNSPECIFIED) codecpar->color_space     = AVCOL_SPC_BT709;
        return;
    }

    if (cat == 2) {
        /* BT.601 — PAL (BT470BG/GAMMA28) vs NTSC (SMPTE170M) */
        int pal = (pri == AVCOL_PRI_BT470BG || pri == AVCOL_PRI_BT470M ||
                   trc == AVCOL_TRC_GAMMA22  || trc == AVCOL_TRC_GAMMA28 ||
                   spc == AVCOL_SPC_BT470BG);
        if (pri == AVCOL_PRI_UNSPECIFIED)
            codecpar->color_primaries = pal ? AVCOL_PRI_BT470BG  : AVCOL_PRI_SMPTE170M;
        if (trc == AVCOL_TRC_UNSPECIFIED)
            codecpar->color_trc       = pal ? AVCOL_TRC_GAMMA28  : AVCOL_TRC_SMPTE170M;
        if (spc == AVCOL_SPC_UNSPECIFIED)
            codecpar->color_space     = pal ? AVCOL_SPC_BT470BG  : AVCOL_SPC_SMPTE170M;
        return;
    }

    /* cat == 3: HDR — only fill when TRC unambiguously identifies the standard */
    if (trc == AVCOL_TRC_SMPTE2084 || trc == AVCOL_TRC_ARIB_STD_B67) {
        if (pri == AVCOL_PRI_UNSPECIFIED) codecpar->color_primaries = AVCOL_PRI_BT2020;
        if (spc == AVCOL_SPC_UNSPECIFIED) codecpar->color_space     = AVCOL_SPC_BT2020_NCL;
    }
    /* Without TRC, can't distinguish HDR10 from HLG — leave as-is */
}
