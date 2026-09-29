/*
 * avpipe_waveform.c
 */

#include <inttypes.h>
#include <math.h>
#include <stdlib.h>
#include <string.h>

#include <libavutil/common.h>
#include <libavutil/samplefmt.h>

#include "avpipe_waveform.h"
#include "elv_log.h"

/* Conversions to the int16 domain, matching what ffmpeg's s16 output produces for each source format */
static inline int16_t wf_conv_u8(uint8_t v)   { return (int16_t) (((int) v - 128) << 8); }
static inline int16_t wf_conv_s16(int16_t v)  { return v; }
static inline int16_t wf_conv_s32(int32_t v)  { return (int16_t) (v >> 16); }
static inline int16_t wf_conv_s64(int64_t v)  { return (int16_t) (v >> 48); }
static inline int16_t wf_conv_flt(float v)    { return av_clip_int16(lrintf(v * 32768.0f)); }
static inline int16_t wf_conv_dbl(double v)   { return av_clip_int16(lrint(v * 32768.0)); }

/*
 * Folds samples [offset, offset+run) of every channel into the open bucket. Instantiated once per sample type so the
 * inner loop has no per-sample branching.
 */
#define WAVEFORM_SCAN(T, CONV)                                                                  \
    do {                                                                                        \
        for (int ch = 0; ch < acc->channels; ch++) {                                            \
            const T *p;                                                                         \
            int step;                                                                           \
            if (acc->planar) {                                                                  \
                p = (const T *) frame->extended_data[ch] + offset;                              \
                step = 1;                                                                       \
            } else {                                                                            \
                p = (const T *) frame->extended_data[0] + (size_t) offset * acc->channels + ch; \
                step = acc->channels;                                                           \
            }                                                                                   \
            int16_t lo = acc->cur_min[ch];                                                      \
            int16_t hi = acc->cur_max[ch];                                                      \
            for (int k = 0; k < run; k++, p += step) {                                          \
                int16_t v = CONV(*p);                                                           \
                if (v < lo)                                                                     \
                    lo = v;                                                                     \
                if (v > hi)                                                                     \
                    hi = v;                                                                     \
            }                                                                                   \
            acc->cur_min[ch] = lo;                                                              \
            acc->cur_max[ch] = hi;                                                              \
        }                                                                                       \
    } while (0)

static void
waveform_scan(
    waveform_acc_t *acc,
    const AVFrame *frame,
    int offset,
    int run)
{
    switch (av_get_packed_sample_fmt(acc->sample_fmt)) {
    case AV_SAMPLE_FMT_U8:  WAVEFORM_SCAN(uint8_t, wf_conv_u8);  break;
    case AV_SAMPLE_FMT_S16: WAVEFORM_SCAN(int16_t, wf_conv_s16); break;
    case AV_SAMPLE_FMT_S32: WAVEFORM_SCAN(int32_t, wf_conv_s32); break;
    case AV_SAMPLE_FMT_S64: WAVEFORM_SCAN(int64_t, wf_conv_s64); break;
    case AV_SAMPLE_FMT_FLT: WAVEFORM_SCAN(float,   wf_conv_flt); break;
    case AV_SAMPLE_FMT_DBL: WAVEFORM_SCAN(double,  wf_conv_dbl); break;
    default: break;
    }
}

static void
waveform_reset_bucket(
    waveform_acc_t *acc)
{
    for (int ch = 0; ch < acc->channels; ch++) {
        acc->cur_min[ch] = INT16_MAX;
        acc->cur_max[ch] = INT16_MIN;
    }
    acc->bucket_pos = 0;
    acc->bucket_samples = 0;
}

static int
waveform_supported_format(
    enum AVSampleFormat fmt)
{
    switch (av_get_packed_sample_fmt(fmt)) {
    case AV_SAMPLE_FMT_U8:
    case AV_SAMPLE_FMT_S16:
    case AV_SAMPLE_FMT_S32:
    case AV_SAMPLE_FMT_S64:
    case AV_SAMPLE_FMT_FLT:
    case AV_SAMPLE_FMT_DBL:
        return 1;
    default:
        return 0;
    }
}

static int
waveform_acc_init(
    waveform_acc_t *acc,
    coderctx_t *decoder_context,
    int stream_index,
    const AVFrame *frame,
    xcparams_t *params)
{
    acc->stream_index = stream_index;
    acc->sample_fmt = frame->format;
    acc->planar = av_sample_fmt_is_planar(frame->format);
    acc->channels = frame->ch_layout.nb_channels;
    acc->sample_rate = frame->sample_rate;
    acc->time_base = decoder_context->stream[stream_index]->time_base;
    acc->spp = params->waveform_samples_per_pixel;
    acc->batch_buckets = params->waveform_batch_buckets;

    if (!waveform_supported_format(frame->format) ||
        acc->channels < 1 || acc->channels > WAVEFORM_MAX_CHANNELS ||
        acc->sample_rate <= 0 || acc->spp <= 0 || acc->batch_buckets <= 0) {
        elv_err("waveform: unsupported audio stream_index=%d fmt=%s channels=%d rate=%d spp=%d batch=%d url=%s",
            stream_index, av_get_sample_fmt_name(frame->format), acc->channels, acc->sample_rate,
            acc->spp, acc->batch_buckets, params->url);
        return eav_audio_sample;
    }

    /* Align the bucket grid: bucket i covers samples [i*spp, (i+1)*spp) of the whole stream */
    int64_t start = params->waveform_start_sample;
    if (start < 0 && frame->pts != AV_NOPTS_VALUE)
        start = av_rescale_q(frame->pts, acc->time_base, (AVRational) {1, acc->sample_rate});
    if (start < 0) {
        if (start != params->waveform_start_sample)
            elv_warn("waveform: negative start sample %"PRId64", aligning at 0, url=%s", start, params->url);
        start = 0;
    }
    acc->bucket_index = start / acc->spp;
    waveform_reset_bucket(acc);
    acc->bucket_pos = (int) (start % acc->spp);
    acc->bucket_start_pts = frame->pts;
    acc->next_pts = frame->pts;

    acc->stats.minmax = (int16_t *) calloc((size_t) acc->batch_buckets * acc->channels * 2, sizeof(int16_t));
    if (!acc->stats.minmax)
        return eav_mem_alloc;
    acc->stats.stream_index = stream_index;
    acc->stats.sample_rate = acc->sample_rate;
    acc->stats.channels = acc->channels;
    acc->stats.channel_layout = frame->ch_layout.order == AV_CHANNEL_ORDER_NATIVE ? frame->ch_layout.u.mask : 0;
    acc->stats.samples_per_pixel = acc->spp;
    acc->stats.time_base = acc->time_base;
    acc->stats.first_bucket_index = acc->bucket_index;
    acc->stats.start_pts = frame->pts;

    elv_log("waveform: init stream_index=%d fmt=%s channels=%d rate=%d spp=%d batch=%d start_sample=%"PRId64
        " first_bucket=%"PRId64" bucket_pos=%d pts=%"PRId64" tb=%d/%d url=%s",
        stream_index, av_get_sample_fmt_name(frame->format), acc->channels, acc->sample_rate, acc->spp,
        acc->batch_buckets, start, acc->bucket_index, acc->bucket_pos, frame->pts,
        acc->time_base.num, acc->time_base.den, params->url);
    return eav_success;
}

/* Hands the current batch to the input handler's stater, which must copy it before returning */
static int
waveform_emit(
    waveform_acc_t *acc,
    coderctx_t *decoder_context,
    int is_last)
{
    int rc = 0;
    ioctx_t *inctx = decoder_context->inctx;
    avpipe_io_handler_t *in_handlers = decoder_context->in_handlers;

    acc->stats.n_buckets = acc->batch_fill;
    acc->stats.total_samples = acc->total_samples;
    acc->stats.is_last = is_last;

    if (inctx && in_handlers && in_handlers->avpipe_stater) {
        inctx->waveform = &acc->stats;
        rc = in_handlers->avpipe_stater(inctx, acc->stream_index, in_stat_audio_waveform);
        inctx->waveform = NULL;
    }

    acc->batch_fill = 0;
    acc->stats.first_bucket_index = acc->bucket_index;
    acc->stats.start_pts = acc->bucket_start_pts;

    if (rc < 0) {
        elv_err("waveform: stater rejected batch stream_index=%d rc=%d", acc->stream_index, rc);
        return eav_write_frame;
    }
    return eav_success;
}

static int
waveform_close_bucket(
    waveform_acc_t *acc,
    coderctx_t *decoder_context)
{
    int16_t *out = acc->stats.minmax + (size_t) acc->batch_fill * acc->channels * 2;

    for (int ch = 0; ch < acc->channels; ch++) {
        out[2 * ch] = acc->cur_min[ch];
        out[2 * ch + 1] = acc->cur_max[ch];
    }
    if (acc->batch_fill == 0) {
        acc->stats.first_bucket_index = acc->bucket_index;
        acc->stats.start_pts = acc->bucket_start_pts;
    }
    acc->batch_fill++;
    acc->stats.last_bucket_samples = acc->bucket_samples;

    acc->bucket_index++;
    waveform_reset_bucket(acc);

    if (acc->batch_fill == acc->batch_buckets)
        return waveform_emit(acc, decoder_context, 0);
    return eav_success;
}

int
waveform_acc_push(
    coderctx_t *decoder_context,
    int audio_index,
    int stream_index,
    const AVFrame *frame,
    xcparams_t *params)
{
    int rc;

    if (audio_index < 0 || audio_index >= MAX_STREAMS)
        return eav_stream_index;

    waveform_acc_t *acc = decoder_context->waveform_acc[audio_index];
    if (!acc) {
        acc = (waveform_acc_t *) calloc(1, sizeof(waveform_acc_t));
        if (!acc)
            return eav_mem_alloc;
        rc = waveform_acc_init(acc, decoder_context, stream_index, frame, params);
        if (rc != eav_success) {
            free(acc->stats.minmax);
            free(acc);
            return rc;
        }
        decoder_context->waveform_acc[audio_index] = acc;
    }

    if (frame->format != acc->sample_fmt ||
        frame->ch_layout.nb_channels != acc->channels ||
        frame->sample_rate != acc->sample_rate) {
        elv_err("waveform: audio parameters changed stream_index=%d fmt=%s channels=%d rate=%d url=%s",
            stream_index, av_get_sample_fmt_name(frame->format), frame->ch_layout.nb_channels,
            frame->sample_rate, params->url);
        return eav_audio_sample;
    }

    AVRational sample_tb = {1, acc->sample_rate};
    int64_t pts = frame->pts;
    if (pts == AV_NOPTS_VALUE) {
        pts = acc->next_pts;
    } else if (acc->next_pts != AV_NOPTS_VALUE) {
        int64_t tolerance = av_rescale_q(frame->nb_samples, sample_tb, acc->time_base);
        int64_t diff = pts - acc->next_pts;
        if (diff > tolerance || diff < -tolerance)
            elv_warn("waveform: pts discontinuity stream_index=%d expected=%"PRId64" got=%"PRId64" url=%s",
                stream_index, acc->next_pts, pts, params->url);
    }

    int offset = 0;
    while (offset < frame->nb_samples) {
        int run = FFMIN(frame->nb_samples - offset, acc->spp - acc->bucket_pos);
        if (acc->bucket_samples == 0)
            acc->bucket_start_pts = pts + av_rescale_q(offset, sample_tb, acc->time_base);
        waveform_scan(acc, frame, offset, run);
        acc->bucket_pos += run;
        acc->bucket_samples += run;
        acc->total_samples += run;
        offset += run;
        if (acc->bucket_pos == acc->spp) {
            rc = waveform_close_bucket(acc, decoder_context);
            if (rc != eav_success)
                return rc;
        }
    }
    acc->next_pts = pts + av_rescale_q(frame->nb_samples, sample_tb, acc->time_base);
    return eav_success;
}

int
waveform_acc_flush(
    coderctx_t *decoder_context,
    int audio_index,
    int stream_index)
{
    int rc;

    if (audio_index < 0 || audio_index >= MAX_STREAMS)
        return eav_stream_index;

    waveform_acc_t *acc = decoder_context->waveform_acc[audio_index];
    if (!acc || acc->flushed)
        return eav_success;
    acc->flushed = 1;

    if (acc->bucket_samples > 0) {
        rc = waveform_close_bucket(acc, decoder_context);
        if (rc != eav_success)
            return rc;
    }

    elv_log("waveform: flush stream_index=%d total_samples=%"PRId64" next_bucket=%"PRId64,
        stream_index, acc->total_samples, acc->bucket_index);
    return waveform_emit(acc, decoder_context, 1);
}

void
waveform_acc_free(
    coderctx_t *decoder_context)
{
    for (int i = 0; i < MAX_STREAMS; i++) {
        waveform_acc_t *acc = decoder_context->waveform_acc[i];
        if (!acc)
            continue;
        free(acc->stats.minmax);
        free(acc);
        decoder_context->waveform_acc[i] = NULL;
    }
}
