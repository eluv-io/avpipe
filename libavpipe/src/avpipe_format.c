/*
 * avpipe_format.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libavutil/log.h>
#include <libavutil/pixdesc.h>

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

int
num_audio_output(
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int n_decoder_auido = decoder_context ? decoder_context->n_audio : 0;
    if (!params)
        return 0;

    if (params->xc_type == xc_audio_merge || params->xc_type == xc_audio_join || params->xc_type == xc_audio_pan)
        return 1;

    return params->n_audio > 0 ? params->n_audio : n_decoder_auido;
}

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
    int channels = codec_ctx->channels;
    int skip_bytes = samples_to_skip * sample_size;
    for (int ch = 0; ch < channels; ch++) {
        if (frame->data[ch]) {
            frame->data[ch] += skip_bytes;
        }
    }

    frame->nb_samples -= samples_to_skip;
    return eav_success;
}