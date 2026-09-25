/*
 * avpipe_waveform.h
 *
 * Audio waveform accumulation for xc_audio_waveform. Decoded audio frames are folded into buckets of
 * waveform_samples_per_pixel samples, keeping the minimum and maximum sample value of every channel, and delivered
 * to the input handler in batches through the in_stat_audio_waveform stat (see audio_waveform_stats_t).
 */

#ifndef AVPIPE_WAVEFORM_H
#define AVPIPE_WAVEFORM_H

#include "avpipe_xc.h"

typedef struct waveform_acc_t {
    int                 stream_index;
    enum AVSampleFormat sample_fmt;
    int                 planar;
    int                 channels;
    int                 sample_rate;
    AVRational          time_base;          /* Stream time base of the frame pts */
    int                 spp;                /* Samples per bucket */
    int                 batch_buckets;      /* Buckets per stat callback */

    int                 bucket_pos;         /* Grid position inside the open bucket, 0..spp */
    int                 bucket_samples;     /* Decoded samples folded into the open bucket */
    int64_t             bucket_index;       /* Absolute index of the open bucket */
    int64_t             bucket_start_pts;   /* pts of the first decoded sample of the open bucket */
    int16_t             cur_min[WAVEFORM_MAX_CHANNELS];
    int16_t             cur_max[WAVEFORM_MAX_CHANNELS];

    int64_t             next_pts;           /* Expected pts of the next frame, for discontinuity warnings */
    int64_t             total_samples;
    int                 batch_fill;         /* Buckets stored in stats.minmax */
    int                 flushed;

    audio_waveform_stats_t stats;           /* Stat payload; stats.minmax holds batch_buckets buckets */
} waveform_acc_t;

/*
 * Folds a decoded audio frame into the accumulator of the given decoder audio index, allocating the accumulator on
 * the first frame. Emits a stat whenever a batch fills up. Returns eav_write_frame if the stater rejected the batch.
 */
int
waveform_acc_push(
    coderctx_t *decoder_context,
    int audio_index,
    int stream_index,
    const AVFrame *frame,
    xcparams_t *params);

/*
 * Closes the open bucket, if it holds any samples, and emits the terminal batch with is_last set. Safe to call more
 * than once and without a preceding push.
 */
int
waveform_acc_flush(
    coderctx_t *decoder_context,
    int audio_index,
    int stream_index);

void
waveform_acc_free(
    coderctx_t *decoder_context);

#endif
