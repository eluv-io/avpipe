/*
 * avpipe_filters.h
 *
 * Declarations for audio/video filter functions.
 */

#ifndef AVPIPE_FILTERS_H
#define AVPIPE_FILTERS_H

#include "avpipe_xc.h"

int
init_video_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

int
init_audio_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

int
init_audio_pan_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context);

int
init_audio_merge_pan_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context);

int
init_audio_join_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

/**
 * @brief   Find and store the crop filter context in decoder_context for
 *          per-frame send_command.
 *
 * @param   decoder_context  Decoder context with filter graph.
 * @param   params           Transcoding parameters (url for logging).
 * @return  0 on success, eav_filter_init if crop filter not found.
 */
int
crop_get_context(
    coderctx_t *decoder_context,
    xcparams_t *params);

/**
 * @brief   Calculate crop width for vertical video (9:16 aspect ratio).
 *
 * @param   source_height  Source video height in pixels.
 * @return  Crop width in pixels (source_height * 9 / 16).
 */
int
crop_calc_width(
    int source_height);

/**
 * @brief   Send crop x command to the filter graph for vertical video.
 *
 * @param   decoder_context  Decoder context with filter graph and crop filter ctx.
 * @param   params           Transcoding parameters (vertical_data, url).
 * @param   frame_number     Current frame number (1-based).
 * @param   source_width     Source video width in pixels.
 */
void
crop_send_command(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

#endif /* AVPIPE_FILTERS_H */
