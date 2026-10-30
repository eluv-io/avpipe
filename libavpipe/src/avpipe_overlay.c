/*
 * avpipe_overlay.c
 *
 * A graphic burned into the output video, driven by timed events.
 *
 * - The layout is a list of slots. Image slots are drawn into one RGBA canvas, the bounding box of their rects,
 *   which an overlay filter blends onto the scaled frame. Each text/clock slot becomes a named drawtext filter
 *   after the overlay, created with empty text. So images are always under text.
 * - Each event is the complete state of every slot from its pts on. On every decoded frame the latest event with
 *   pts <= frame pts is in effect; a slot whose text differs from the last one sent gets one send_command, and
 *   a change in the images redraws the canvas.
 * - The canvas is pushed into the overlay filter on every frame with that frame's pts, by reference, so the
 *   filter always has a frame to match the main frame with and never stalls.
 * - Frames are matched by pts in presentation order, so the same input gives the same switch frame in every
 *   rendition, whatever the frame rate, drops or reordering.
 */

#include <string.h>
#include <limits.h>
#include <inttypes.h>

#include <libavcodec/avcodec.h>
#include <libavfilter/avfilter.h>
#include <libavfilter/buffersrc.h>
#include <libswscale/swscale.h>

#include "avpipe_overlay.h"
#include "elv_log.h"

static char *
ov_strdup(
    const char *s)
{
    return s ? strdup(s) : NULL;
}

ov_params_t *
overlay_alloc(
    xcparams_t *params,
    int64_t timescale,
    int nslots,
    int nevents)
{
    overlay_free(&params->overlay);

    ov_params_t *ov = calloc(1, sizeof(*ov));
    if (!ov)
        return NULL;
    ov->timescale = timescale;
    ov->nslots = nslots;
    ov->nevents = nevents;
    ov->slots = calloc(nslots > 0 ? nslots : 1, sizeof(ov_slot_t));
    ov->events = calloc(nevents > 0 ? nevents : 1, sizeof(ov_event_t));
    if (!ov->slots || !ov->events) {
        overlay_free(&ov);
        return NULL;
    }
    for (int i = 0; i < nslots; i++)
        ov->slots[i].default_image = OV_IMAGE_HIDDEN;
    for (int i = 0; i < nevents; i++) {
        ov_event_t *ev = &ov->events[i];
        ev->text = calloc(nslots > 0 ? nslots : 1, sizeof(char *));
        ev->clock = calloc(nslots > 0 ? nslots : 1, sizeof(ov_clock_t));
        ev->image = calloc(nslots > 0 ? nslots : 1, sizeof(int));
        if (!ev->text || !ev->clock || !ev->image) {
            overlay_free(&ov);
            return NULL;
        }
        for (int j = 0; j < nslots; j++)
            ev->image[j] = OV_IMAGE_DEFAULT;
    }
    params->overlay = ov;
    return ov;
}

int
overlay_add_image(
    xcparams_t *params,
    const uint8_t *png,
    int png_len)
{
    ov_params_t *ov = params->overlay;
    if (!ov || !png || png_len <= 0)
        return -1;
    /* Events carry the full state, so the same picture arrives again with every later event */
    for (int i = 0; i < ov->nimages; i++) {
        if (ov->images[i].png_len == png_len && !memcmp(ov->images[i].png, png, png_len))
            return i;
    }
    ov_image_t *images = realloc(ov->images, (size_t) (ov->nimages + 1) * sizeof(ov_image_t));
    if (!images)
        return -1;
    ov->images = images;
    ov_image_t *img = &ov->images[ov->nimages];
    img->png = malloc(png_len);
    if (!img->png)
        return -1;
    memcpy(img->png, png, png_len);
    img->png_len = png_len;
    return ov->nimages++;
}

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
    int fade_ms)
{
    ov_params_t *ov = params->overlay;
    if (!ov || index < 0 || index >= ov->nslots)
        return eav_param;
    ov_slot_t *slot = &ov->slots[index];
    slot->type = (ov_slot_type) type;
    slot->x = x;
    slot->y = y;
    slot->size = size;
    slot->h = h;
    slot->align = (ov_align) align;
    slot->clock_format = (ov_clock_format) clock_format;
    slot->default_image = default_image;
    slot->fade_ms = fade_ms;
    free(slot->font_file);
    free(slot->color);
    slot->font_file = ov_strdup(font_file);
    slot->color = ov_strdup(color);
    return eav_success;
}

int
overlay_set_event(
    xcparams_t *params,
    int index,
    int64_t seq,
    int64_t pts,
    int hidden)
{
    ov_params_t *ov = params->overlay;
    if (!ov || index < 0 || index >= ov->nevents)
        return eav_param;
    ov->events[index].seq = seq;
    ov->events[index].pts = pts;
    ov->events[index].hidden = hidden;
    return eav_success;
}

int
overlay_append_event(
    xcparams_t *params,
    int64_t seq,
    int64_t pts,
    int hidden)
{
    ov_params_t *ov = params->overlay;
    if (!ov)
        return -1;
    if (ov->nevents > 0 && pts < ov->events[ov->nevents - 1].pts) {
        elv_err("Overlay: appended event seq=%"PRId64" pts=%"PRId64" is before the last event, url=%s",
            seq, pts, params->url);
        return -1;
    }
    ov_event_t *events = realloc(ov->events, (size_t) (ov->nevents + 1) * sizeof(ov_event_t));
    if (!events)
        return -1;
    ov->events = events;

    ov_event_t *ev = &ov->events[ov->nevents];
    memset(ev, 0, sizeof(*ev));
    ev->seq = seq;
    ev->pts = pts;
    ev->hidden = hidden;
    ev->text = calloc(ov->nslots, sizeof(char *));
    ev->clock = calloc(ov->nslots, sizeof(ov_clock_t));
    ev->image = calloc(ov->nslots, sizeof(int));
    if (!ev->text || !ev->clock || !ev->image) {
        free(ev->text);
        free(ev->clock);
        free(ev->image);
        return -1;
    }
    for (int j = 0; j < ov->nslots; j++)
        ev->image[j] = OV_IMAGE_DEFAULT;
    return ov->nevents++;
}

void
overlay_set_through(
    xcparams_t *params,
    int64_t through_pts)
{
    if (params->overlay)
        params->overlay->through_pts = through_pts;
}

int
overlay_set_event_text(
    xcparams_t *params,
    int event,
    int slot,
    const char *text)
{
    ov_params_t *ov = params->overlay;
    if (!ov || event < 0 || event >= ov->nevents || slot < 0 || slot >= ov->nslots)
        return eav_param;
    free(ov->events[event].text[slot]);
    ov->events[event].text[slot] = ov_strdup(text);
    return eav_success;
}

int
overlay_set_event_clock(
    xcparams_t *params,
    int event,
    int slot,
    int64_t ms,
    int running,
    int up)
{
    ov_params_t *ov = params->overlay;
    if (!ov || event < 0 || event >= ov->nevents || slot < 0 || slot >= ov->nslots)
        return eav_param;
    ov_clock_t *clock = &ov->events[event].clock[slot];
    clock->set = 1;
    clock->ms = ms;
    clock->running = running;
    clock->up = up;
    return eav_success;
}

int
overlay_set_event_image(
    xcparams_t *params,
    int event,
    int slot,
    int image)
{
    ov_params_t *ov = params->overlay;
    if (!ov || event < 0 || event >= ov->nevents || slot < 0 || slot >= ov->nslots)
        return eav_param;
    ov->events[event].image[slot] = image;
    return eav_success;
}

ov_params_t *
overlay_copy(
    const ov_params_t *src)
{
    if (!src)
        return NULL;

    xcparams_t tmp = { 0 };
    ov_params_t *ov = overlay_alloc(&tmp, src->timescale, src->nslots, src->nevents);
    if (!ov)
        return NULL;
    ov->through_pts = src->through_pts;
    ov->reader = src->reader;
    ov->reader_handle = src->reader_handle;
    for (int i = 0; i < src->nslots; i++) {
        ov->slots[i] = src->slots[i];
        ov->slots[i].font_file = ov_strdup(src->slots[i].font_file);
        ov->slots[i].color = ov_strdup(src->slots[i].color);
    }
    for (int i = 0; i < src->nimages; i++) {
        if (overlay_add_image(&tmp, src->images[i].png, src->images[i].png_len) < 0) {
            overlay_free(&ov);
            return NULL;
        }
    }
    for (int i = 0; i < src->nevents; i++) {
        ov->events[i].seq = src->events[i].seq;
        ov->events[i].pts = src->events[i].pts;
        ov->events[i].hidden = src->events[i].hidden;
        for (int j = 0; j < src->nslots; j++) {
            ov->events[i].text[j] = ov_strdup(src->events[i].text[j]);
            ov->events[i].clock[j] = src->events[i].clock[j];
            ov->events[i].image[j] = src->events[i].image[j];
        }
    }
    return ov;
}

void
overlay_free(
    ov_params_t **ovp)
{
    ov_params_t *ov = ovp ? *ovp : NULL;
    if (!ov)
        return;
    for (int i = 0; i < ov->nslots; i++) {
        free(ov->slots[i].font_file);
        free(ov->slots[i].color);
    }
    for (int i = 0; i < ov->nimages; i++)
        free(ov->images[i].png);
    for (int i = 0; i < ov->nevents; i++) {
        if (ov->events[i].text) {
            for (int j = 0; j < ov->nslots; j++)
                free(ov->events[i].text[j]);
            free(ov->events[i].text);
        }
        free(ov->events[i].clock);
        free(ov->events[i].image);
    }
    free(ov->slots);
    free(ov->images);
    free(ov->events);
    free(ov);
    *ovp = NULL;
}

int
overlay_validate(
    xcparams_t *params)
{
    ov_params_t *ov = params->overlay;
    if (!ov)
        return eav_success;

    const char *url = params->url ? params->url : "";
    if (params->bypass_transcoding) {
        elv_err("Incompatible params - overlay requires transcoding (bypass must be disabled), url=%s", url);
        return eav_param;
    }
    if (params->rotate > 0 || params->deinterlace != dif_none) {
        elv_err("Incompatible params - overlay not supported with rotate or deinterlacing, url=%s", url);
        return eav_param;
    }
    if ((params->watermark_text && *params->watermark_text != '\0') ||
        (params->watermark_timecode && *params->watermark_timecode != '\0') ||
        (params->watermark_overlay && params->watermark_overlay[0] != '\0')) {
        elv_err("Incompatible params - overlay not supported with watermark, url=%s", url);
        return eav_param;
    }
    if (ov->timescale <= 0 || ov->timescale > INT32_MAX) {
        elv_err("Overlay: invalid timescale=%"PRId64", url=%s", ov->timescale, url);
        return eav_param;
    }
    if (ov->nslots <= 0) {
        elv_err("Overlay: no slots, url=%s", url);
        return eav_param;
    }
    for (int i = 0; i < ov->nimages; i++) {
        if (!ov->images[i].png || ov->images[i].png_len <= 0) {
            elv_err("Overlay: image=%d is empty, url=%s", i, url);
            return eav_param;
        }
    }
    for (int i = 0; i < ov->nslots; i++) {
        ov_slot_t *slot = &ov->slots[i];
        if (slot->size <= 0 || slot->h < 0 || slot->fade_ms < 0) {
            elv_err("Overlay: slot=%d needs a size, url=%s", i, url);
            return eav_param;
        }
        switch (slot->type) {
        case ov_slot_text:
        case ov_slot_clock:
            if (!slot->font_file || !*slot->font_file) {
                elv_err("Overlay: slot=%d needs a font file, url=%s", i, url);
                return eav_param;
            }
            if (slot->clock_format != ov_clock_mmss && slot->clock_format != ov_clock_hmmss) {
                elv_err("Overlay: slot=%d invalid clock format=%d, url=%s", i, slot->clock_format, url);
                return eav_param;
            }
            break;
        case ov_slot_image:
            if (slot->default_image < OV_IMAGE_HIDDEN || slot->default_image >= ov->nimages) {
                elv_err("Overlay: slot=%d invalid default image=%d, url=%s", i, slot->default_image, url);
                return eav_param;
            }
            break;
        default:
            elv_err("Overlay: slot=%d unknown type=%d, url=%s", i, slot->type, url);
            return eav_param;
        }
    }
    for (int i = 0; i < ov->nevents; i++) {
        if (i > 0 && ov->events[i].pts < ov->events[i-1].pts) {
            elv_err("Overlay: events not sorted at index=%d, url=%s", i, url);
            return eav_param;
        }
        for (int j = 0; j < ov->nslots; j++) {
            int img = ov->events[i].image[j];
            if (img < OV_IMAGE_DEFAULT || img >= ov->nimages) {
                elv_err("Overlay: event=%d slot=%d invalid image=%d, url=%s", i, j, img, url);
                return eav_param;
            }
        }
    }
    return eav_success;
}

/* Images */

static AVFrame *
ov_alloc_rgba(
    int w,
    int h)
{
    AVFrame *f = av_frame_alloc();
    if (!f)
        return NULL;
    f->format = AV_PIX_FMT_RGBA;
    f->width = w;
    f->height = h;
    if (av_frame_get_buffer(f, 0) < 0) {
        av_frame_free(&f);
        return NULL;
    }
    return f;
}

/* Convert and scale src to an RGBA frame of w x h */
static AVFrame *
ov_to_rgba(
    const AVFrame *src,
    int w,
    int h)
{
    AVFrame *dst = ov_alloc_rgba(w, h);
    if (!dst)
        return NULL;
    struct SwsContext *sws = sws_getContext(src->width, src->height, src->format,
        w, h, AV_PIX_FMT_RGBA, SWS_BILINEAR, NULL, NULL, NULL);
    if (!sws) {
        av_frame_free(&dst);
        return NULL;
    }
    sws_scale(sws, (const uint8_t * const *) src->data, src->linesize, 0, src->height, dst->data, dst->linesize);
    sws_freeContext(sws);
    return dst;
}

static AVFrame *
ov_decode_png(
    const uint8_t *png,
    int png_len,
    int index,
    const char *url)
{
    const AVCodec *codec = avcodec_find_decoder(AV_CODEC_ID_PNG);
    AVCodecContext *ctx = codec ? avcodec_alloc_context3(codec) : NULL;
    AVPacket *pkt = av_packet_alloc();
    AVFrame *frame = av_frame_alloc();
    AVFrame *rgba = NULL;
    int ret = AVERROR(ENOMEM);

    if (!ctx || !pkt || !frame)
        goto done;
    if ((ret = avcodec_open2(ctx, codec, NULL)) < 0)
        goto done;
    if ((ret = av_new_packet(pkt, png_len)) < 0)
        goto done;
    memcpy(pkt->data, png, png_len);
    if ((ret = avcodec_send_packet(ctx, pkt)) < 0)
        goto done;
    if ((ret = avcodec_receive_frame(ctx, frame)) < 0) {
        /* An image decoder may hand the frame out only once flushed */
        avcodec_send_packet(ctx, NULL);
        if ((ret = avcodec_receive_frame(ctx, frame)) < 0)
            goto done;
    }
    if (frame->width <= 0 || frame->height <= 0) {
        ret = AVERROR_INVALIDDATA;
        goto done;
    }
    rgba = ov_to_rgba(frame, frame->width, frame->height);
    if (!rgba)
        ret = AVERROR(ENOMEM);

done:
    if (!rgba)
        elv_err("Overlay: failed to decode image=%d ret=%d url=%s", index, ret, url);
    av_frame_free(&frame);
    av_packet_free(&pkt);
    avcodec_free_context(&ctx);
    return rgba;
}

/* Straight-alpha "over" of src onto dst at (x0, y0), clipped to dst, with src's alpha scaled by mul/255 */
static void
ov_blit(
    AVFrame *dst,
    const AVFrame *src,
    int x0,
    int y0,
    int mul)
{
    for (int y = 0; y < src->height; y++) {
        int dy = y0 + y;
        if (dy < 0 || dy >= dst->height)
            continue;
        const uint8_t *s = src->data[0] + y * src->linesize[0];
        uint8_t *d = dst->data[0] + dy * dst->linesize[0];
        for (int x = 0; x < src->width; x++, s += 4) {
            int dx = x0 + x;
            if (dx < 0 || dx >= dst->width)
                continue;
            uint8_t *p = d + dx * 4;
            int a = mul == 255 ? s[3] : s[3] * mul / 255;
            if (a == 0)
                continue;
            if (a == 255) {
                memcpy(p, s, 4);
                continue;
            }
            int ia = 255 - a;
            for (int c = 0; c < 3; c++)
                p[c] = (s[c] * a + p[c] * ia + 127) / 255;
            p[3] = a + (p[3] * ia + 127) / 255;
        }
    }
}

/* Scale img to the rect's width, keeping its aspect ratio; with fit_height also inside the rect's height */
static AVFrame *
ov_scale_to_rect(
    const AVFrame *img,
    const int *rect,
    int fit_height)
{
    int w = rect[2];
    int h = img->height * rect[2] / img->width;
    if (fit_height && h > rect[3]) {
        h = rect[3];
        w = img->width * rect[3] / img->height;
    }
    return ov_to_rgba(img, w > 0 ? w : 1, h > 0 ? h : 1);
}

/* Library image j scaled for slot i, decoded on first use and kept in *cache while the slot shows it */
static AVFrame *
ov_scaled_image(
    ov_state_t *st,
    ov_params_t *ov,
    int i,
    int j,
    AVFrame **cache,
    int *cache_for,
    const char *url)
{
    if (*cache_for != j || !*cache) {
        AVFrame *img = ov_decode_png(ov->images[j].png, ov->images[j].png_len, j, url);
        if (!img)
            return NULL;
        av_frame_free(cache);
        *cache = ov_scale_to_rect(img, &st->slot_rect[i * 4], ov->slots[i].h > 0);
        av_frame_free(&img);
        *cache_for = *cache ? j : -1;
    }
    return *cache;
}

/* Draw a new canvas for the current row at pts. The old one may still be referenced by the filter, so it is
 * replaced. A fading slot draws the old image at 1-t and the new at t; two opaque images dip a little in the
 * middle of a cross-fade, which is fine for a scoreboard. */
static int
ov_draw_canvas(
    ov_state_t *st,
    ov_params_t *ov,
    const char *url,
    int64_t pts)
{
    AVFrame *canvas = ov_alloc_rgba(st->canvas_w, st->canvas_h);
    if (!canvas)
        return eav_mem_alloc;
    for (int y = 0; y < canvas->height; y++)
        memset(canvas->data[0] + y * canvas->linesize[0], 0, (size_t) canvas->width * 4);

    for (int i = 0; i < ov->nslots; i++) {
        if (ov->slots[i].type != ov_slot_image)
            continue;
        int *rect = &st->slot_rect[i * 4];
        int x = rect[0] - st->canvas_x, y = rect[1] - st->canvas_y;
        int mul = 255;
        if (st->fade_from[i] != OV_FADE_NONE && pts - st->fade_start[i] < st->fade_tb[i]) {
            mul = (int) ((pts - st->fade_start[i]) * 255 / st->fade_tb[i]);
            if (st->fade_from[i] >= 0) {
                AVFrame *f = ov_scaled_image(st, ov, i, st->fade_from[i], &st->fade_scaled[i],
                    &st->fade_scaled_for[i], url);
                if (!f) {
                    av_frame_free(&canvas);
                    return eav_overlay;
                }
                ov_blit(canvas, f, x, y, 255 - mul);
            }
        }
        int j = st->canvas_row[i];
        if (j < 0 || mul == 0)
            continue;
        AVFrame *f = ov_scaled_image(st, ov, i, j, &st->slot_scaled[i], &st->slot_scaled_for[i], url);
        if (!f) {
            av_frame_free(&canvas);
            return eav_overlay;
        }
        ov_blit(canvas, f, x, y, mul);
    }

    av_frame_free(&st->canvas);
    st->canvas = canvas;
    st->canvas_stale = 0;
    elv_dbg("Overlay: canvas drawn %dx%d url=%s", canvas->width, canvas->height, url);
    return eav_success;
}

int
overlay_prepare(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params)
{
    ov_params_t *ov = params->overlay;
    if (!ov)
        return eav_success;

    ov_state_t *st = calloc(1, sizeof(*st));
    if (!st)
        return eav_mem_alloc;
    decoder_context->overlay = st;
    st->cursor = -1;
    st->nslots = ov->nslots;
    st->canvas_stale = 1;
    st->event_pts = calloc(ov->nevents > 0 ? ov->nevents : 1, sizeof(int64_t));
    st->text_ctx = calloc(ov->nslots, sizeof(AVFilterContext *));
    st->text_sent = calloc(ov->nslots, sizeof(char *));
    st->slot_rect = calloc((size_t) ov->nslots * 4, sizeof(int));
    st->canvas_row = calloc(ov->nslots, sizeof(int));
    st->slot_scaled = calloc(ov->nslots, sizeof(AVFrame *));
    st->slot_scaled_for = calloc(ov->nslots, sizeof(int));
    st->fade_tb = calloc(ov->nslots, sizeof(int64_t));
    st->fade_from = calloc(ov->nslots, sizeof(int));
    st->fade_start = calloc(ov->nslots, sizeof(int64_t));
    st->fade_scaled = calloc(ov->nslots, sizeof(AVFrame *));
    st->fade_scaled_for = calloc(ov->nslots, sizeof(int));
    if (!st->event_pts || !st->text_ctx || !st->text_sent || !st->slot_rect || !st->canvas_row ||
        !st->slot_scaled || !st->slot_scaled_for || !st->fade_tb || !st->fade_from || !st->fade_start ||
        !st->fade_scaled || !st->fade_scaled_for) {
        overlay_free_state(decoder_context);
        return eav_mem_alloc;
    }

    /* Lay out the image slots at the output size; the canvas is their bounding box, even-sized */
    AVCodecContext *enc = encoder_context->codec_context[encoder_context->video_stream_index];
    int x0 = INT_MAX, y0 = INT_MAX, x1 = 0, y1 = 0;
    for (int i = 0; i < ov->nslots; i++) {
        ov_slot_t *slot = &ov->slots[i];
        st->canvas_row[i] = OV_IMAGE_HIDDEN;
        st->slot_scaled_for[i] = -1;
        st->fade_from[i] = OV_FADE_NONE;
        st->fade_scaled_for[i] = -1;
        if (slot->type != ov_slot_image)
            continue;
        int *rect = &st->slot_rect[i * 4];
        rect[0] = (int) (slot->x * enc->width);
        rect[1] = (int) (slot->y * enc->height);
        rect[2] = (int) (slot->size * enc->width);
        if (rect[2] < 1)
            rect[2] = 1;
        if (slot->h > 0) {
            rect[3] = (int) (slot->h * enc->height);
        } else {
            /* No height given: as tall as the tallest image the slot can show at start, its default and the
             * ones the start events name. An image delivered later is fitted into that rect. */
            rect[3] = 1;
            for (int j = 0; j < ov->nimages; j++) {
                int shown = slot->default_image == j;
                for (int k = 0; k < ov->nevents && !shown; k++)
                    shown = ov->events[k].image[i] == j;
                if (!shown)
                    continue;
                AVFrame *img = ov_decode_png(ov->images[j].png, ov->images[j].png_len, j, params->url);
                if (!img) {
                    overlay_free_state(decoder_context);
                    return eav_param;
                }
                int h = rect[2] * img->height / img->width;
                av_frame_free(&img);
                if (h > rect[3])
                    rect[3] = h;
            }
        }
        if (rect[3] < 1)
            rect[3] = 1;
        if (rect[0] < x0) x0 = rect[0];
        if (rect[1] < y0) y0 = rect[1];
        if (rect[0] + rect[2] > x1) x1 = rect[0] + rect[2];
        if (rect[1] + rect[3] > y1) y1 = rect[1] + rect[3];
    }
    if (x1 > 0 || y1 > 0) {
        st->canvas_x = x0 & ~1;
        st->canvas_y = y0 & ~1;
        st->canvas_w = (x1 - st->canvas_x + 1) & ~1;
        st->canvas_h = (y1 - st->canvas_y + 1) & ~1;
    }

    elv_log("Overlay: slots=%d images=%d events=%d canvas=%dx%d at %d,%d url=%s",
        ov->nslots, ov->nimages, ov->nevents, st->canvas_w, st->canvas_h, st->canvas_x, st->canvas_y, params->url);
    return eav_success;
}

int
overlay_add_canvas_source(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVFilterInOut *outputs)
{
    ov_state_t *st = decoder_context->overlay;
    if (!st || st->canvas_w <= 0)
        return 0;

    AVRational tb = encoder_context->codec_context[decoder_context->video_stream_index]->time_base;
    char args[256];
    snprintf(args, sizeof(args), "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=1/1",
        st->canvas_w, st->canvas_h, AV_PIX_FMT_RGBA, tb.num, tb.den);
    int ret = avfilter_graph_create_filter(&st->canvas_src, avfilter_get_by_name("buffer"), "ov",
        args, NULL, decoder_context->video_filter_graph);
    if (ret < 0) {
        elv_err("Overlay: cannot create the canvas source, args=%s err=%d", args, ret);
        return ret;
    }

    AVFilterInOut *out = avfilter_inout_alloc();
    if (!out)
        return AVERROR(ENOMEM);
    out->name = av_strdup("ov");
    out->filter_ctx = st->canvas_src;
    out->pad_idx = 0;
    out->next = NULL;
    while (outputs->next)
        outputs = outputs->next;
    outputs->next = out;
    return 0;
}

/* Quote s for the filter graph parser: inside '...' only the quote itself needs escaping */
static int
ov_quote(
    char *dst,
    size_t dst_sz,
    const char *s)
{
    size_t n = 0;
    for (; *s; s++) {
        const char *rep = (*s == '\'') ? "'\\''" : NULL;
        size_t len = rep ? strlen(rep) : 1;
        if (n + len + 1 > dst_sz)
            return -1;
        if (rep)
            memcpy(dst + n, rep, len);
        else
            dst[n] = *s;
        n += len;
    }
    dst[n] = '\0';
    return 0;
}

static int
ov_append(
    char *filter_str,
    size_t filter_str_sz,
    const char *fragment,
    const char *url)
{
    size_t filter_len = strlen(filter_str);
    size_t n = strlen(fragment);
    if (filter_len + n + 1 > filter_str_sz) {
        elv_err("Overlay: filter string is full, url=%s", url);
        return eav_filter_string_init;
    }
    memcpy(filter_str + filter_len, fragment, n + 1);
    return eav_success;
}

int
overlay_append_filter(
    char *filter_str,
    size_t filter_str_sz,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params)
{
    ov_params_t *ov = params->overlay;
    ov_state_t *st = decoder_context->overlay;
    if (!ov || !st)
        return eav_success;

    AVCodecContext *enc = encoder_context->codec_context[encoder_context->video_stream_index];
    int enc_w = enc->width;
    int enc_h = enc->height;
    char buf[1536];
    int rc;

    /* The chain so far ends unlabelled; [m] names it so the overlay can take the canvas [ov] as its second input */
    if (st->canvas_w > 0) {
        snprintf(buf, sizeof(buf), "[m];[m][ov]overlay=x=%d:y=%d:format=auto:eof_action=repeat:repeatlast=1",
            st->canvas_x, st->canvas_y);
        if ((rc = ov_append(filter_str, filter_str_sz, buf, params->url)) != eav_success)
            return rc;
    }

    for (int i = 0; i < ov->nslots; i++) {
        ov_slot_t *slot = &ov->slots[i];
        if (slot->type == ov_slot_image)
            continue;

        char font[1024];
        char x_expr[64];
        int font_size = (int) (slot->size * enc_h);
        int x = (int) (slot->x * enc_w);
        int y = (int) (slot->y * enc_h);

        if (font_size < 1)
            font_size = 1;
        if (ov_quote(font, sizeof(font), slot->font_file) < 0) {
            elv_err("Overlay: font path too long, slot=%d, url=%s", i, params->url);
            return eav_filter_string_init;
        }
        switch (slot->align) {
        case ov_align_center:
            snprintf(x_expr, sizeof(x_expr), "%d-tw/2", x);
            break;
        case ov_align_right:
            snprintf(x_expr, sizeof(x_expr), "%d-tw", x);
            break;
        default:
            snprintf(x_expr, sizeof(x_expr), "%d", x);
            break;
        }

        /* The text starts empty and is only ever set through send_command, so it never meets the graph parser */
        int n = snprintf(buf, sizeof(buf),
            ",drawtext@ov%d=fontfile='%s':text='':expansion=none:fontsize=%d:fontcolor=%s:x=%s:y=%d",
            i, font, font_size, slot->color && *slot->color ? slot->color : "white", x_expr, y);
        if (n < 0 || n >= (int) sizeof(buf)) {
            elv_err("Overlay: filter fragment too long, slot=%d, url=%s", i, params->url);
            return eav_filter_string_init;
        }
        if ((rc = ov_append(filter_str, filter_str_sz, buf, params->url)) != eav_success)
            return rc;
    }
    return eav_success;
}

/* A through pts near INT64_MAX means final; rescaling it would overflow */
static int64_t
ov_rescale_through(
    int64_t through_pts,
    AVRational from,
    AVRational to)
{
    if (through_pts >= INT64_MAX / 4)
        return INT64_MAX;
    return av_rescale_q(through_pts, from, to);
}

/*
 * Ask the reader for the events up to frame_pts, then rescale what it appended. The reader blocks until the
 * recording's through pts covers the frame, or fails; a frame is never drawn with events that may be incomplete.
 */
static int
ov_fetch(
    ov_state_t *st,
    ov_params_t *ov,
    xcparams_t *params,
    int64_t frame_pts)
{
    AVRational ov_tb = { 1, (int) ov->timescale };
    int64_t at_least = av_rescale_q_rnd(frame_pts, st->tb, ov_tb, AV_ROUND_UP);
    int64_t after_seq = ov->nevents > 0 ? ov->events[ov->nevents - 1].seq : 0;

    st->fetches++;
    int rc = ov->reader(ov->reader_handle, params, after_seq, at_least);
    if (rc < 0) {
        elv_err("Overlay: event source failed rc=%d after_seq=%"PRId64" at_least=%"PRId64" frame_pts=%"PRId64" url=%s",
            rc, after_seq, at_least, frame_pts, params->url);
        return rc == OV_READER_CANCELLED ? eav_cancelled : eav_overlay;
    }

    if (ov->nevents > st->nbound) {
        int64_t *event_pts = realloc(st->event_pts, (size_t) ov->nevents * sizeof(int64_t));
        if (!event_pts)
            return eav_mem_alloc;
        st->event_pts = event_pts;
        for (int i = st->nbound; i < ov->nevents; i++)
            st->event_pts[i] = av_rescale_q(ov->events[i].pts, ov_tb, st->tb);
        st->nbound = ov->nevents;
    }
    st->through_pts_tb = ov_rescale_through(ov->through_pts, ov_tb, st->tb);
    elv_log("Overlay: fetched events=%d through_pts=%"PRId64" after_seq=%"PRId64" at_least=%"PRId64" url=%s",
        ov->nevents, ov->through_pts, after_seq, at_least, params->url);

    if (frame_pts > st->through_pts_tb) {
        elv_err("Overlay: event source stopped short, through_pts=%"PRId64" frame_pts=%"PRId64" url=%s",
            st->through_pts_tb, frame_pts, params->url);
        return eav_overlay;
    }
    return eav_success;
}

int
overlay_bind(
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    ov_params_t *ov = params->overlay;
    ov_state_t *st = decoder_context->overlay;
    if (!ov || !st)
        return eav_success;

    AVRational tb = decoder_context->stream[decoder_context->video_stream_index]->time_base;
    AVRational ov_tb = { 1, (int) ov->timescale };
    st->tb = tb;
    for (int i = 0; i < ov->nevents; i++)
        st->event_pts[i] = av_rescale_q(ov->events[i].pts, ov_tb, tb);
    st->nbound = ov->nevents;
    for (int i = 0; i < ov->nslots; i++) {
        if (ov->slots[i].type == ov_slot_image && ov->slots[i].fade_ms > 0)
            st->fade_tb[i] = av_rescale_q(ov->slots[i].fade_ms, (AVRational) { 1, 1000 }, tb);
    }
    st->through_pts_tb = ov->reader ? ov_rescale_through(ov->through_pts, ov_tb, tb) : INT64_MAX;

    AVFilterGraph *graph = decoder_context->video_filter_graph;
    for (int i = 0; i < ov->nslots; i++) {
        if (ov->slots[i].type == ov_slot_image)
            continue;
        char name[32];
        snprintf(name, sizeof(name), "drawtext@ov%d", i);
        for (unsigned int f = 0; f < graph->nb_filters; f++) {
            if (!strcmp(graph->filters[f]->name, name)) {
                st->text_ctx[i] = graph->filters[f];
                break;
            }
        }
        if (!st->text_ctx[i]) {
            elv_err("Overlay: filter %s not found in graph, url=%s", name, params->url);
            return eav_filter_init;
        }
    }

    elv_log("Overlay: bound timescale=%"PRId64" stream_tb=%d/%d reader=%d through_pts=%"PRId64" url=%s",
        ov->timescale, tb.num, tb.den, ov->reader != NULL, ov->through_pts, params->url);
    return eav_success;
}

/*
 * The clock's text at elapsed_ms after its event. A countdown shows the second it is in (2.3 s left reads 00:03)
 * and stops at zero; a count-up shows the seconds completed.
 */
static void
ov_clock_text(
    char *buf,
    size_t buf_sz,
    const ov_slot_t *slot,
    const ov_clock_t *clock,
    int64_t elapsed_ms)
{
    int64_t ms = clock->ms;
    if (clock->running)
        ms += clock->up ? elapsed_ms : -elapsed_ms;
    if (ms < 0)
        ms = 0;
    int64_t s = clock->up ? ms / 1000 : (ms + 999) / 1000;
    if (slot->clock_format == ov_clock_hmmss)
        snprintf(buf, buf_sz, "%"PRId64":%02"PRId64":%02"PRId64, s / 3600, (s / 60) % 60, s % 60);
    else
        snprintf(buf, buf_sz, "%02"PRId64":%02"PRId64, s / 60, s % 60);
}

int
overlay_apply(
    coderctx_t *decoder_context,
    xcparams_t *params,
    AVFrame *frame)
{
    ov_state_t *st = decoder_context->overlay;
    ov_params_t *ov = params->overlay;
    if (!st || !ov || frame->pts == AV_NOPTS_VALUE)
        return eav_success;

    if (ov->reader && frame->pts > st->through_pts_tb) {
        int rc = ov_fetch(st, ov, params, frame->pts);
        if (rc != eav_success)
            return rc;
    }

    while (st->cursor + 1 < ov->nevents && st->event_pts[st->cursor + 1] <= frame->pts)
        st->cursor++;
    if (st->cursor < 0)
        return eav_success;

    ov_event_t *ev = &ov->events[st->cursor];
    int64_t elapsed_ms = av_rescale_q(frame->pts - st->event_pts[st->cursor], st->tb, (AVRational) { 1, 1000 });

    /* Every slot's wanted value is recomputed per frame (clocks tick); only a change costs anything */
    for (int i = 0; i < ov->nslots; i++) {
        ov_slot_t *slot = &ov->slots[i];

        if (slot->type == ov_slot_image) {
            int want = ev->hidden ? OV_IMAGE_HIDDEN : ev->image[i];
            if (want == OV_IMAGE_DEFAULT)
                want = slot->default_image;
            if (want != st->canvas_row[i]) {
                if (st->fade_tb[i] > 0) {
                    st->fade_from[i] = st->canvas_row[i];
                    st->fade_start[i] = st->event_pts[st->cursor];
                }
                st->canvas_row[i] = want;
                st->canvas_stale = 1;
                elv_log("Overlay: event=%d event_pts=%"PRId64" frame_pts=%"PRId64" slot=%d image=%d fade=%d url=%s",
                    st->cursor, ev->pts, frame->pts, i, want, st->fade_tb[i] > 0, params->url);
            }
            if (st->fade_from[i] != OV_FADE_NONE) {
                /* Redrawn on every frame of the fade, and once more with the fade over */
                st->canvas_stale = 1;
                if (frame->pts - st->fade_start[i] >= st->fade_tb[i])
                    st->fade_from[i] = OV_FADE_NONE;
            }
            continue;
        }

        if (!st->text_ctx[i])
            continue;
        char clock_buf[32];
        const char *want = "";
        if (!ev->hidden) {
            if (slot->type == ov_slot_clock) {
                if (ev->clock[i].set) {
                    ov_clock_text(clock_buf, sizeof(clock_buf), slot, &ev->clock[i], elapsed_ms);
                    want = clock_buf;
                }
            } else if (ev->text[i]) {
                want = ev->text[i];
            }
        }
        const char *have = st->text_sent[i] ? st->text_sent[i] : "";
        if (!strcmp(want, have))
            continue;

        char res[128];
        int ret = avfilter_graph_send_command(decoder_context->video_filter_graph,
            st->text_ctx[i]->name, "text", want, res, sizeof(res), 0);
        if (ret < 0) {
            elv_err("Overlay: failed to set text, event=%d slot=%d ret=%d url=%s",
                st->cursor, i, ret, params->url);
            return eav_overlay;
        }
        free(st->text_sent[i]);
        st->text_sent[i] = strdup(want);
        st->commands++;
        if (slot->type == ov_slot_clock)
            elv_dbg("Overlay: event=%d frame_pts=%"PRId64" slot=%d clock='%s' url=%s",
                st->cursor, frame->pts, i, want, params->url);
        else
            elv_log("Overlay: event=%d event_pts=%"PRId64" frame_pts=%"PRId64" slot=%d text='%s' url=%s",
                st->cursor, ev->pts, frame->pts, i, want, params->url);
    }
    return eav_success;
}

int
overlay_push_canvas(
    coderctx_t *decoder_context,
    xcparams_t *params,
    AVFrame *frame)
{
    ov_state_t *st = decoder_context->overlay;
    if (!st || st->canvas_w <= 0)
        return eav_success;

    if (st->canvas_stale || !st->canvas) {
        int rc = ov_draw_canvas(st, params->overlay, params->url, frame->pts);
        if (rc != eav_success)
            return rc;
    }
    st->canvas->pts = frame->pts;
    if (av_buffersrc_add_frame_flags(st->canvas_src, st->canvas, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
        elv_err("Overlay: failed to push the canvas, pts=%"PRId64" url=%s", frame->pts, params->url);
        return eav_overlay;
    }
    return eav_success;
}

int
overlay_close_sources(
    coderctx_t *decoder_context)
{
    ov_state_t *st = decoder_context->overlay;
    if (!st || !st->canvas_src)
        return 0;
    int ret = av_buffersrc_add_frame_flags(decoder_context->video_buffersrc_ctx, NULL, 0);
    if (ret < 0)
        elv_warn("Overlay: closing the video source failed ret=%d", ret);
    ret = av_buffersrc_add_frame_flags(st->canvas_src, NULL, 0);
    if (ret < 0)
        elv_warn("Overlay: closing the canvas source failed ret=%d", ret);
    return 1;
}

void
overlay_free_state(
    coderctx_t *decoder_context)
{
    ov_state_t *st = decoder_context->overlay;
    if (!st)
        return;
    if (st->text_sent) {
        for (int i = 0; i < st->nslots; i++)
            free(st->text_sent[i]);
    }
    for (int i = 0; i < st->nslots; i++) {
        if (st->slot_scaled)
            av_frame_free(&st->slot_scaled[i]);
        if (st->fade_scaled)
            av_frame_free(&st->fade_scaled[i]);
    }
    av_frame_free(&st->canvas);
    free(st->fade_scaled_for);
    free(st->fade_scaled);
    free(st->fade_start);
    free(st->fade_from);
    free(st->fade_tb);
    free(st->slot_scaled_for);
    free(st->slot_scaled);
    free(st->canvas_row);
    free(st->slot_rect);
    free(st->text_sent);
    free(st->text_ctx);
    free(st->event_pts);
    free(st);
    decoder_context->overlay = NULL;
}
