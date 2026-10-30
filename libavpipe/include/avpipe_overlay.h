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
 * Builders for the Go side, which validates and flattens the params: every event carries a value for every slot
 * index. The strings are copied.
 */
ov_params_t *
overlay_alloc(
    xcparams_t *params,
    int64_t timescale,
    int nslots,
    int nevents);

/**
 * @brief   Append a PNG to the image library, at job start or from a reader while the job runs.
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

int
overlay_set_event(
    xcparams_t *params,
    int index,
    int64_t seq,
    int64_t pts,
    int hidden);

/**
 * @brief   Append an event, for a reader. Its slot values are set with the overlay_set_event_* functions.
 * @return  The new event's index, or -1 when pts is before the last event or memory runs out.
 */
int
overlay_append_event(
    xcparams_t *params,
    int64_t seq,
    int64_t pts,
    int hidden);

void
overlay_set_through(
    xcparams_t *params,
    int64_t through_pts);

int
overlay_set_event_text(
    xcparams_t *params,
    int event,
    int slot,
    const char *text);

int
overlay_set_event_clock(
    xcparams_t *params,
    int event,
    int slot,
    int64_t ms,
    int running,
    int up);

int
overlay_set_event_image(
    xcparams_t *params,
    int event,
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
 *          output size, which the filter string needs. A slot without a height is sized to the images it can show
 *          at start, which are decoded here for that; everything else is decoded when first shown.
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
 * @brief   Bind the state to the configured filter graph: finds the drawtext filters and rescales the event pts
 *          to the input video stream time base.
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
 * @brief   At EOF, close the main and canvas buffer sources so the overlay filter releases the frame it holds.
 * @return  1 when the sources were closed and the sink must be drained, 0 when there is no canvas.
 */
int
overlay_close_sources(
    coderctx_t *decoder_context);

/**
 * @brief   Apply the latest event with pts <= frame->pts. Call once per decoded video frame, before the frame is
 *          rescaled to the encoder time base. Only slots whose value changed get a send_command.
 * @return  eav_success, or eav_overlay when a slot update is rejected.
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
