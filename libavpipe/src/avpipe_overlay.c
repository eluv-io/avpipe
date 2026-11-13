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
#include <libavutil/time.h>
#include <libswscale/swscale.h>

#include "avpipe_overlay.h"
#include "elv_log.h"

/* A frame that waits for its event asks the source again after this long */
#define OV_WAIT_SLICE_US    10000

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
    int64_t wait_ms,
    int on_timeout)
{
    overlay_free(&params->overlay);

    ov_params_t *ov = calloc(1, sizeof(*ov));
    if (!ov)
        return NULL;
    ov->timescale = timescale;
    ov->nslots = nslots;
    ov->wait_ms = wait_ms;
    ov->on_timeout = (ov_timeout) on_timeout;
    ov->slots = calloc(nslots > 0 ? nslots : 1, sizeof(ov_slot_t));
    if (!ov->slots) {
        overlay_free(&ov);
        return NULL;
    }
    for (int i = 0; i < nslots; i++)
        ov->slots[i].default_image = OV_IMAGE_HIDDEN;
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

ov_event_t *
overlay_event_alloc(
    int nslots)
{
    ov_event_t *ev = calloc(1, sizeof(*ev));
    if (!ev)
        return NULL;
    int n = nslots > 0 ? nslots : 1;
    ev->text = calloc(n, sizeof(char *));
    ev->clock = calloc(n, sizeof(ov_clock_t));
    ev->image = calloc(n, sizeof(int));
    if (!ev->text || !ev->clock || !ev->image) {
        overlay_event_free(&ev, nslots);
        return NULL;
    }
    overlay_event_clear(ev, nslots);
    return ev;
}

void
overlay_event_free(
    ov_event_t **evp,
    int nslots)
{
    ov_event_t *ev = evp ? *evp : NULL;
    if (!ev)
        return;
    if (ev->text) {
        for (int j = 0; j < nslots; j++)
            free(ev->text[j]);
    }
    free(ev->text);
    free(ev->clock);
    free(ev->image);
    free(ev);
    *evp = NULL;
}

void
overlay_event_clear(
    ov_event_t *ev,
    int nslots)
{
    ev->seq = -1;
    ev->pts = 0;
    ev->hidden = 0;
    for (int j = 0; j < nslots; j++) {
        free(ev->text[j]);
        ev->text[j] = NULL;
        ev->clock[j].set = 0;
        ev->image[j] = OV_IMAGE_DEFAULT;
    }
}

void
overlay_event_set(
    ov_event_t *ev,
    int64_t seq,
    int64_t pts,
    int hidden)
{
    ev->seq = seq;
    ev->pts = pts;
    ev->hidden = hidden;
}

int
overlay_event_set_text(
    ov_event_t *ev,
    int nslots,
    int slot,
    const char *text)
{
    if (!ev || slot < 0 || slot >= nslots)
        return eav_param;
    free(ev->text[slot]);
    ev->text[slot] = ov_strdup(text);
    return eav_success;
}

int
overlay_event_set_clock(
    ov_event_t *ev,
    int nslots,
    int slot,
    int64_t ms,
    int running,
    int up)
{
    if (!ev || slot < 0 || slot >= nslots)
        return eav_param;
    ov_clock_t *clock = &ev->clock[slot];
    clock->set = 1;
    clock->ms = ms;
    clock->running = running;
    clock->up = up;
    return eav_success;
}

int
overlay_event_set_image(
    ov_event_t *ev,
    int nslots,
    int slot,
    int image)
{
    if (!ev || slot < 0 || slot >= nslots)
        return eav_param;
    ev->image[slot] = image;
    return eav_success;
}

ov_params_t *
overlay_copy(
    const ov_params_t *src)
{
    if (!src)
        return NULL;

    xcparams_t tmp = { 0 };
    ov_params_t *ov = overlay_alloc(&tmp, src->timescale, src->nslots, src->wait_ms, src->on_timeout);
    if (!ov)
        return NULL;
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
    free(ov->slots);
    free(ov->images);
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
    if (!params->event_at) {
        elv_err("Overlay: no event source, url=%s", url);
        return eav_param;
    }
    if (ov->wait_ms < 0 || (ov->on_timeout != ov_timeout_fail && ov->on_timeout != ov_timeout_hold)) {
        elv_err("Overlay: invalid wait_ms=%"PRId64" or on_timeout=%d, url=%s", ov->wait_ms, ov->on_timeout, url);
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
            if (slot->clock_format != ov_clock_mmss && slot->clock_format != ov_clock_hmmss &&
                slot->clock_format != ov_clock_game && slot->clock_format != ov_clock_shot) {
                elv_err("Overlay: slot=%d invalid clock format=%d, url=%s", i, slot->clock_format, url);
                return eav_param;
            }
            break;
        case ov_slot_image:
            if (slot->h <= 0) {
                elv_err("Overlay: slot=%d is an image slot and needs a height, url=%s", i, url);
                return eav_param;
            }
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
    return eav_success;
}

/* Images */

/* The library index an event shows in an image slot, with hidden and the slot default resolved */
static int
ov_event_image(
    const ov_event_t *ev,
    const ov_slot_t *slot,
    int i)
{
    int want = ev->hidden ? OV_IMAGE_HIDDEN : ev->image[i];
    return want == OV_IMAGE_DEFAULT ? slot->default_image : want;
}

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

/*
 * Straight-alpha "over" of src onto dst at (x0, y0), clipped to dst, with src's alpha scaled by mul/255. Both
 * frames hold straight (un-premultiplied) colour, so the result colour is divided by the result alpha.
 */
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
            if (a == 255 || p[3] == 0) {
                memcpy(p, s, 3);
                p[3] = a;
                continue;
            }
            int db = p[3] * (255 - a);              // destination weight, x255
            int oa = a * 255 + db;                  // out alpha, x255
            for (int c = 0; c < 3; c++)
                p[c] = (s[c] * a * 255 + p[c] * db + oa / 2) / oa;
            p[3] = (oa + 127) / 255;
        }
    }
}

/*
 * Cross-fade: dst = (1 - t) * old + t * new, in premultiplied terms, written back as straight alpha. Either
 * image may be NULL (nothing). Both are placed at the rect's top left; dst is the rect's size and is overwritten.
 */
static void
ov_lerp(
    AVFrame *dst,
    const AVFrame *old,
    const AVFrame *new,
    int t)
{
    for (int y = 0; y < dst->height; y++) {
        const uint8_t *o = old && y < old->height ? old->data[0] + y * old->linesize[0] : NULL;
        const uint8_t *n = new && y < new->height ? new->data[0] + y * new->linesize[0] : NULL;
        uint8_t *d = dst->data[0] + y * dst->linesize[0];
        for (int x = 0; x < dst->width; x++, d += 4) {
            int oa = o && x < old->width ? o[x * 4 + 3] * (255 - t) : 0;   // x255
            int na = n && x < new->width ? n[x * 4 + 3] * t : 0;
            int a = oa + na;
            if (a == 0) {
                memset(d, 0, 4);
                continue;
            }
            for (int c = 0; c < 3; c++)
                d[c] = ((oa ? o[x * 4 + c] * oa : 0) + (na ? n[x * 4 + c] * na : 0) + a / 2) / a;
            d[3] = (a + 127) / 255;
        }
    }
}

/* Scale img to the largest size that fits inside the rect, keeping its aspect ratio */
static AVFrame *
ov_scale_to_rect(
    const AVFrame *img,
    const int *rect)
{
    int w = rect[2];
    int h = img->height * rect[2] / img->width;
    if (h > rect[3]) {
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
        *cache = ov_scale_to_rect(img, &st->slot_rect[i * 4]);
        av_frame_free(&img);
        *cache_for = *cache ? j : -1;
    }
    return *cache;
}

/*
 * Draw the canvas for the current row at ts (overlay timescale) and convert it to the frame the filter takes.
 * The pushed frame is replaced, never edited in place: the filter may still hold the previous one. A fading
 * slot is the cross-fade of what it showed and what it shows, composed as one layer.
 */
static int
ov_draw_canvas(
    ov_state_t *st,
    ov_params_t *ov,
    const char *url,
    int64_t ts)
{
    AVFrame *rgba = st->canvas_rgba;
    for (int y = 0; y < rgba->height; y++)
        memset(rgba->data[0] + y * rgba->linesize[0], 0, (size_t) rgba->width * 4);

    for (int i = 0; i < ov->nslots; i++) {
        if (ov->slots[i].type != ov_slot_image)
            continue;
        int *rect = &st->slot_rect[i * 4];
        int x = rect[0] - st->canvas_x, y = rect[1] - st->canvas_y;
        int j = st->canvas_row[i];
        AVFrame *new = NULL, *old = NULL;
        if (j >= 0 && !(new = ov_scaled_image(st, ov, i, j, &st->slot_scaled[i], &st->slot_scaled_for[i], url)))
            return eav_overlay;

        if (st->fade_from[i] != OV_FADE_NONE && ts - st->fade_start[i] < st->fade_ts[i]) {
            int t = (int) ((ts - st->fade_start[i]) * 255 / st->fade_ts[i]);
            if (st->fade_from[i] >= 0 && !(old = ov_scaled_image(st, ov, i, st->fade_from[i],
                    &st->fade_scaled[i], &st->fade_scaled_for[i], url)))
                return eav_overlay;
            if (old && new) {
                AVFrame *mix = ov_alloc_rgba(rect[2], rect[3]);
                if (!mix)
                    return eav_mem_alloc;
                ov_lerp(mix, old, new, t);
                ov_blit(rgba, mix, x, y, 255);
                av_frame_free(&mix);
            } else if (old) {
                ov_blit(rgba, old, x, y, 255 - t);
            } else if (new) {
                ov_blit(rgba, new, x, y, t);
            }
            continue;
        }
        if (new)
            ov_blit(rgba, new, x, y, 255);
    }

    AVFrame *out = av_frame_alloc();
    if (!out)
        return eav_mem_alloc;
    out->format = st->canvas_fmt;
    out->width = rgba->width;
    out->height = rgba->height;
    if (av_frame_get_buffer(out, 0) < 0) {
        av_frame_free(&out);
        return eav_mem_alloc;
    }
    if (st->canvas_fmt == AV_PIX_FMT_RGBA) {
        av_frame_copy(out, rgba);
    } else {
        st->canvas_sws = sws_getCachedContext(st->canvas_sws, rgba->width, rgba->height, AV_PIX_FMT_RGBA,
            out->width, out->height, st->canvas_fmt, SWS_BILINEAR, NULL, NULL, NULL);
        if (!st->canvas_sws) {
            av_frame_free(&out);
            return eav_mem_alloc;
        }
        sws_scale(st->canvas_sws, (const uint8_t * const *) rgba->data, rgba->linesize, 0, rgba->height,
            out->data, out->linesize);
    }
    av_frame_free(&st->canvas);
    st->canvas = out;
    st->canvas_stale = 0;
    elv_dbg("Overlay: canvas drawn %dx%d ts=%"PRId64" url=%s", out->width, out->height, ts, url);
    return eav_success;
}

/* The overlay filter's blend format for the encoder's pixel format, and the canvas format that goes with it.
 * Under "auto" the whole main frame would be converted to RGBA and back on every frame. */
static const char *
ov_blend_format(
    enum AVPixelFormat enc_fmt,
    enum AVPixelFormat *canvas_fmt)
{
    switch (enc_fmt) {
    case AV_PIX_FMT_YUV420P:    *canvas_fmt = AV_PIX_FMT_YUVA420P;   return "yuv420";
    case AV_PIX_FMT_YUV420P10:  *canvas_fmt = AV_PIX_FMT_YUVA420P10; return "yuv420p10";
    case AV_PIX_FMT_YUV422P:    *canvas_fmt = AV_PIX_FMT_YUVA422P;   return "yuv422";
    case AV_PIX_FMT_YUV422P10:  *canvas_fmt = AV_PIX_FMT_YUVA422P10; return "yuv422p10";
    case AV_PIX_FMT_YUV444P:    *canvas_fmt = AV_PIX_FMT_YUVA444P;   return "yuv444";
    case AV_PIX_FMT_YUV444P10:  *canvas_fmt = AV_PIX_FMT_YUVA444P10; return "yuv444p10";
    default:                    *canvas_fmt = AV_PIX_FMT_RGBA;       return "auto";
    }
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
    st->nslots = ov->nslots;
    st->canvas_stale = 1;
    st->next = overlay_event_alloc(ov->nslots);
    st->text_ctx = calloc(ov->nslots, sizeof(AVFilterContext *));
    st->text_sent = calloc(ov->nslots, sizeof(char *));
    st->slot_rect = calloc((size_t) ov->nslots * 4, sizeof(int));
    st->canvas_row = calloc(ov->nslots, sizeof(int));
    st->slot_scaled = calloc(ov->nslots, sizeof(AVFrame *));
    st->slot_scaled_for = calloc(ov->nslots, sizeof(int));
    st->fade_ts = calloc(ov->nslots, sizeof(int64_t));
    st->fade_from = calloc(ov->nslots, sizeof(int));
    st->fade_start = calloc(ov->nslots, sizeof(int64_t));
    st->fade_scaled = calloc(ov->nslots, sizeof(AVFrame *));
    st->fade_scaled_for = calloc(ov->nslots, sizeof(int));
    if (!st->next || !st->text_ctx || !st->text_sent || !st->slot_rect || !st->canvas_row ||
        !st->slot_scaled || !st->slot_scaled_for || !st->fade_ts || !st->fade_from || !st->fade_start ||
        !st->fade_scaled || !st->fade_scaled_for) {
        overlay_free_state(decoder_context);
        return eav_mem_alloc;
    }

    /* Every image known at start must decode, so a bad one fails the job before any output */
    for (int j = 0; j < ov->nimages; j++) {
        AVFrame *img = ov_decode_png(ov->images[j].png, ov->images[j].png_len, j, params->url);
        if (!img) {
            overlay_free_state(decoder_context);
            return eav_param;
        }
        av_frame_free(&img);
    }

    /* Lay out the image slots at the output size from the layout alone; the canvas is their bounding box,
     * even-sized. Nothing here depends on which images the events name. */
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
        /* Until the first event, an image slot shows its default, as an event that names none would */
        st->canvas_row[i] = slot->default_image;
        if (slot->fade_ms > 0)
            st->fade_ts[i] = slot->fade_ms * ov->timescale / 1000;
        int *rect = &st->slot_rect[i * 4];
        rect[0] = (int) (slot->x * enc->width);
        rect[1] = (int) (slot->y * enc->height);
        rect[2] = (int) (slot->size * enc->width);
        rect[3] = (int) (slot->h * enc->height);
        if (rect[2] < 1)
            rect[2] = 1;
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
        st->canvas_rgba = ov_alloc_rgba(st->canvas_w, st->canvas_h);
        if (!st->canvas_rgba) {
            overlay_free_state(decoder_context);
            return eav_mem_alloc;
        }
    }
    ov_blend_format(enc->pix_fmt, &st->canvas_fmt);

    elv_log("Overlay: slots=%d images=%d wait_ms=%"PRId64" on_timeout=%s canvas=%dx%d at %d,%d fmt=%s url=%s",
        ov->nslots, ov->nimages, ov->wait_ms, ov->on_timeout == ov_timeout_hold ? "hold" : "fail",
        st->canvas_w, st->canvas_h, st->canvas_x, st->canvas_y, av_get_pix_fmt_name(st->canvas_fmt), params->url);
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
        st->canvas_w, st->canvas_h, st->canvas_fmt, tb.num, tb.den);
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

    /* The chain so far ends unlabelled; [m] names it so the overlay can take the canvas [ov] as its second input.
     * The blend runs in the encoder's own pixel format, so the main frame is never converted. */
    if (st->canvas_w > 0) {
        enum AVPixelFormat canvas_fmt;
        const char *blend = ov_blend_format(enc->pix_fmt, &canvas_fmt);
        snprintf(buf, sizeof(buf), "[m];[m][ov]overlay=x=%d:y=%d:format=%s:eof_action=repeat:repeatlast=1",
            st->canvas_x, st->canvas_y, blend);
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

/*
 * Ask the source for the event in effect at ts (overlay timescale) and make it current. A frame whose event is
 * not there yet waits in slices, checking the cancel flag, up to wait_ms; then on_timeout decides: fail ends the
 * job, hold keeps the current event and stops waiting for the rest of the job: frames still ask once each and
 * apply an event when it is there, but a source that stalls again costs no second wait, so a live job's total
 * stall is bounded by one wait_ms. A frame is never drawn with an event that may still change.
 */
static int
ov_ask(
    ov_state_t *st,
    ov_params_t *ov,
    xcparams_t *params,
    coderctx_t *decoder_context,
    int64_t ts)
{
    if (st->ended)
        return eav_success;
    int64_t have_seq = st->cur ? st->cur->seq : -1;

    for (;;) {
        st->asks++;
        overlay_event_clear(st->next, ov->nslots);
        int rc = params->event_at(params->event_source, params, EV_KIND_OVERLAY, ts, have_seq, st->next);
        switch (rc) {
        case EV_AT_UNCHANGED:
            st->wait_start_us = 0;
            return eav_success;
        case EV_AT_NEW: {
            /* The event before the new one is kept for transitions; its buffer is refilled on the next ask */
            ov_event_t *spare = st->prev;
            st->prev = st->cur;
            st->cur = st->next;
            st->next = spare ? spare : overlay_event_alloc(ov->nslots);
            if (!st->next)
                return eav_mem_alloc;
            st->wait_start_us = 0;
            elv_log("Overlay: event seq=%"PRId64" pts=%"PRId64" hidden=%d frame_ts=%"PRId64" asks=%d url=%s",
                st->cur->seq, st->cur->pts, st->cur->hidden, ts, st->asks, params->url);
            return eav_success;
        }
        case EV_AT_ENDED:
            st->ended = 1;
            st->wait_start_us = 0;
            elv_log("Overlay: event source ended at frame_ts=%"PRId64" url=%s", ts, params->url);
            return eav_success;
        case EV_AT_NOT_YET:
            break;
        case EV_AT_CANCELLED:
            return eav_cancelled;
        default:
            elv_err("Overlay: event source failed rc=%d frame_ts=%"PRId64" have_seq=%"PRId64" url=%s",
                rc, ts, have_seq, params->url);
            return eav_overlay;
        }

        if (st->holding)
            return eav_success;
        if (decoder_context->cancelled)
            return eav_cancelled;
        int64_t now = av_gettime_relative();
        if (!st->wait_start_us) {
            st->wait_start_us = now;
            st->waits++;
        }
        if (now - st->wait_start_us >= ov->wait_ms * 1000) {
            st->wait_start_us = 0;
            if (ov->on_timeout == ov_timeout_hold) {
                elv_warn("Overlay: no event within %"PRId64" ms, holding seq=%"PRId64" frame_ts=%"PRId64" url=%s",
                    ov->wait_ms, have_seq, ts, params->url);
                st->holding = 1;
                return eav_success;
            }
            elv_err("Overlay: no event within %"PRId64" ms, frame_ts=%"PRId64" have_seq=%"PRId64" url=%s",
                ov->wait_ms, ts, have_seq, params->url);
            return eav_overlay;
        }
        av_usleep(OV_WAIT_SLICE_US);
    }
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

    st->tb = decoder_context->stream[decoder_context->video_stream_index]->time_base;

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

    elv_log("Overlay: bound timescale=%"PRId64" stream_tb=%d/%d url=%s",
        ov->timescale, st->tb.num, st->tb.den, params->url);
    return eav_success;
}

/*
 * The clock's text at elapsed_ms after its event. A countdown shows the second it is in (2.3 s left reads 00:03)
 * and stops at zero; a count-up shows the seconds completed. The game format shows tenths under a minute
 * (59.9 .. 0.0) and m:ss above, as a venue game clock does; the shot format shows tenths under five seconds and
 * whole seconds above, as a shot clock does. A shot clock counting down shows the time left cut off, not the
 * second it is in: 5.9 s left reads 5 and 4.99 s reads 4.9, so the reading changes to tenths as 5.0 passes.
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
    if (slot->clock_format == ov_clock_shot && !clock->up) {
        if (ms < 5000)
            snprintf(buf, buf_sz, "%"PRId64".%"PRId64, ms / 1000, (ms / 100) % 10);
        else
            snprintf(buf, buf_sz, "%"PRId64, ms / 1000);
        return;
    }
    if ((slot->clock_format == ov_clock_game && ms < 60000) || (slot->clock_format == ov_clock_shot && ms < 5000)) {
        int64_t tenths = clock->up ? ms / 100 : (ms + 99) / 100;
        snprintf(buf, buf_sz, "%"PRId64".%"PRId64, tenths / 10, tenths % 10);
        return;
    }
    int64_t s = clock->up ? ms / 1000 : (ms + 999) / 1000;
    if (slot->clock_format == ov_clock_shot) {
        snprintf(buf, buf_sz, "%"PRId64, s);
        return;
    }
    if (slot->clock_format == ov_clock_hmmss)
        snprintf(buf, buf_sz, "%"PRId64":%02"PRId64":%02"PRId64, s / 3600, (s / 60) % 60, s % 60);
    else if (slot->clock_format == ov_clock_game)
        snprintf(buf, buf_sz, "%"PRId64":%02"PRId64, s / 60, s % 60);
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

    /* The frame's time in the overlay timescale, floored: the one number every comparison below uses */
    AVRational ov_tb = { 1, (int) ov->timescale };
    int64_t ts = av_rescale_q_rnd(frame->pts, st->tb, ov_tb, AV_ROUND_DOWN);
    st->frame_ts = ts;

    int rc = ov_ask(st, ov, params, decoder_context, ts);
    if (rc != eav_success)
        return rc;
    ov_event_t *ev = st->cur;
    if (!ev)
        return eav_success;
    int64_t elapsed_ms = (ts - ev->pts) * 1000 / ov->timescale;

    /* Every slot's wanted value is recomputed per frame (clocks tick); only a change costs anything */
    for (int i = 0; i < ov->nslots; i++) {
        ov_slot_t *slot = &ov->slots[i];

        if (slot->type == ov_slot_image) {
            int want = ov_event_image(ev, slot, i);
            if (want != st->canvas_row[i]) {
                /* A transition runs from what the previous event showed, whether or not this job drew it, so a
                 * job that starts inside one draws the same frames as a job that ran through it */
                if (st->fade_ts[i] > 0) {
                    int from = st->prev ? ov_event_image(st->prev, slot, i) : OV_IMAGE_HIDDEN;
                    if (from != want) {
                        st->fade_from[i] = from;
                        st->fade_start[i] = ev->pts;
                    }
                }
                st->canvas_row[i] = want;
                st->canvas_stale = 1;
                elv_log("Overlay: seq=%"PRId64" event_pts=%"PRId64" frame_ts=%"PRId64" slot=%d image=%d fade=%d url=%s",
                    ev->seq, ev->pts, ts, i, want, st->fade_from[i] != OV_FADE_NONE, params->url);
            }
            if (st->fade_from[i] != OV_FADE_NONE) {
                /* Redrawn on every frame of the fade, and once more with the fade over */
                st->canvas_stale = 1;
                if (ts - st->fade_start[i] >= st->fade_ts[i])
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
            elv_err("Overlay: failed to set text, seq=%"PRId64" slot=%d ret=%d url=%s",
                ev->seq, i, ret, params->url);
            return eav_overlay;
        }
        free(st->text_sent[i]);
        st->text_sent[i] = strdup(want);
        st->commands++;
        if (slot->type == ov_slot_clock)
            elv_dbg("Overlay: seq=%"PRId64" frame_ts=%"PRId64" slot=%d clock='%s' url=%s",
                ev->seq, ts, i, want, params->url);
        else
            elv_log("Overlay: seq=%"PRId64" event_pts=%"PRId64" frame_ts=%"PRId64" slot=%d text='%s' url=%s",
                ev->seq, ev->pts, ts, i, want, params->url);
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

    /* frame->pts is in the encoder time base by now; the canvas is timed from frame_ts, set by overlay_apply */
    if (st->canvas_stale || !st->canvas) {
        int rc = ov_draw_canvas(st, params->overlay, params->url, st->frame_ts);
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
    overlay_event_free(&st->cur, st->nslots);
    overlay_event_free(&st->prev, st->nslots);
    overlay_event_free(&st->next, st->nslots);
    av_frame_free(&st->canvas);
    av_frame_free(&st->canvas_rgba);
    sws_freeContext(st->canvas_sws);
    free(st->fade_scaled_for);
    free(st->fade_scaled);
    free(st->fade_start);
    free(st->fade_from);
    free(st->fade_ts);
    free(st->slot_scaled_for);
    free(st->slot_scaled);
    free(st->canvas_row);
    free(st->slot_rect);
    free(st->text_sent);
    free(st->text_ctx);
    free(st);
    decoder_context->overlay = NULL;
}
