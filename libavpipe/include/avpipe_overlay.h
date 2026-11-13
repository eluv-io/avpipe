/*
 * avpipe_overlay.h
 *
 * Burned-in graphics driven by timed events. See avpipe_overlay.c.
 */

#ifndef AVPIPE_OVERLAY_H
#define AVPIPE_OVERLAY_H

#include <stddef.h>

#include "avpipe_xc.h"

/*
 * Builders for the Go side, which validates the layout and resolves events against it. The strings are copied.
 */
ov_params_t *
overlay_alloc(
    xcparams_t *params,
    int64_t timescale,
    int nslots,
    int64_t wait_ms,
    int on_timeout);

/**
 * @brief   Append a PNG to the image library, at job start or when the source brings one while the job runs.
 *          The same bytes are not added twice.
 * @return  The image's index, or -1 on a bad argument or when memory runs out.
 */
int
overlay_add_image(
    xcparams_t *params,
    const uint8_t *png,
    int png_len);

int
overlay_set_slot(
    xcparams_t *params,
    int index,
    int type,
    float x,
    float y,
    float size,
    float h,
    int align,
    const char *font_file,
    const char *color,
    int clock_format,
    int default_image,
    int fade_ms);

/*
 * Events. The source fills the ov_event_t the consumer hands it: header, then a value per slot it sets; a slot it
 * does not set is blank, hidden or the slot's default.
 */
ov_event_t *
overlay_event_alloc(
    int nslots);

void
overlay_event_free(
    ov_event_t **ev,
    int nslots);

/* Reset ev for reuse: no values, images at the slot default */
void
overlay_event_clear(
    ov_event_t *ev,
    int nslots);

void
overlay_event_set(
    ov_event_t *ev,
    int64_t seq,
    int64_t pts,
    int hidden);

int
overlay_event_set_text(
    ov_event_t *ev,
    int nslots,
    int slot,
    const char *text);

int
overlay_event_set_clock(
    ov_event_t *ev,
    int nslots,
    int slot,
    int64_t ms,
    int running,
    int up);

int
overlay_event_set_image(
    ov_event_t *ev,
    int nslots,
    int slot,
    int image);

ov_params_t *
overlay_copy(
    const ov_params_t *ov);

void
overlay_free(
    ov_params_t **ov);

/**
 * @brief   Check the overlay params against the rest of xcparams.
 * @return  eav_success, or eav_param when the overlay cannot be applied.
 */
int
overlay_validate(
    xcparams_t *params);

/**
 * @brief   Allocate the per-transcode state before the filter graph is built and lay out the image slots at the
 *          output size, which the filter string needs. Every library image is decoded once here, so a bad one
 *          fails the job before any output.
 */
int
overlay_prepare(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

/**
 * @brief   Add the canvas buffer source to the graph as the open output "ov", when there are image slots.
 *          outputs is the graph's list of open outputs ("in" first); the new entry is appended to it.
 * @return  0, or a negative libav error.
 */
int
overlay_add_canvas_source(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVFilterInOut *outputs);

/**
 * @brief   Append the overlay filters to the video filter string: an overlay of the canvas when there are image
 *          slots, then one named drawtext per text/clock slot. The text starts empty; overlay_apply() sets it.
 */
int
overlay_append_filter(
    char *filter_str,
    size_t filter_str_sz,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

/**
 * @brief   Bind the state to the configured filter graph: finds the drawtext filters and takes the input video
 *          stream time base the frame pts arrive in.
 */
int
overlay_bind(
    coderctx_t *decoder_context,
    xcparams_t *params);

/**
 * @brief   Push the canvas for the event in effect into the overlay filter with the frame's pts. Call right after
 *          the main frame was pushed, so the overlay filter always has a canvas frame to match it with.
 * @return  eav_success, or eav_overlay when the push fails.
 */
int
overlay_push_canvas(
    coderctx_t *decoder_context,
    xcparams_t *params,
    AVFrame *frame);

/**
 * @brief   Ask the source for the event in effect at frame->pts and apply it. Call once per decoded video frame,
 *          before the frame is rescaled to the encoder time base. Waits for an event that is not there yet, in
 *          short slices with the cancel flag checked, up to the overlay's wait_ms; then on_timeout decides. Only
 *          slots whose value changed get a send_command.
 * @return  eav_success; eav_overlay when the source failed, the event was bad or the wait ran out with
 *          ov_timeout_fail; eav_cancelled when the job was cancelled during a wait.
 */
int
overlay_apply(
    coderctx_t *decoder_context,
    xcparams_t *params,
    AVFrame *frame);

void
overlay_free_state(
    coderctx_t *decoder_context);

#endif /* AVPIPE_OVERLAY_H */
