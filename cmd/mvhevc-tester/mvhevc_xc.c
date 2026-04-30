/*
 * mvhevc_xc.c - MV-HEVC transcoding implementation
 *
 * Implements mvhevc_init(), mvhevc_xc(), mvhevc_fini() modeled after
 * avpipe_init(), avpipe_xc(), avpipe_fini() in avpipe_xc.c.
 *
 * Uses FFmpeg for decoding and x265 API directly for multiview encoding.
 */

#include <stdlib.h>
#include <string.h>

#include <libavutil/imgutils.h>
#include <libavutil/opt.h>

#include "mvhevc_xc.h"

/* ------------------------------------------------------------------ */
/* mvhevc_params defaults                                              */
/* ------------------------------------------------------------------ */
void mvhevc_params_defaults(mvhevc_params *p)
{
    memset(p, 0, sizeof(*p));
    p->scenecut = 40;
    p->bframes  = -1;
}

/* ------------------------------------------------------------------ */
/* Validate xcparams_t for MV-HEVC encoding                           */
/* ------------------------------------------------------------------ */
int mvhevc_validate_params(const xcparams_t *xc)
{
    int ok = 1;

    if (xc->url) {
        fprintf(stderr, "validate: url not supported\n"); ok = 0;
    }
    if (xc->bypass_transcoding) {
        fprintf(stderr, "validate: bypass_transcoding not supported\n"); ok = 0;
    }
    if (xc->format) {
        fprintf(stderr, "validate: format not supported (output is raw HEVC)\n"); ok = 0;
    }
    if (xc->start_time_ts) {
        fprintf(stderr, "validate: start_time_ts not supported\n"); ok = 0;
    }
    if (xc->duration_ts) {
        fprintf(stderr, "validate: duration_ts not supported\n"); ok = 0;
    }
    if (xc->audio_bitrate || xc->sample_rate || xc->channel_layout) {
        fprintf(stderr, "validate: audio parameters not supported\n"); ok = 0;
    }
    if (xc->audio_seg_duration_ts || xc->video_seg_duration_ts || xc->seg_duration) {
        fprintf(stderr, "validate: segment duration not supported\n"); ok = 0;
    }
    if (xc->ecodec) {
        fprintf(stderr, "validate: ecodec not supported (always x265 multiview)\n"); ok = 0;
    }
    if (xc->dcodec) {
        fprintf(stderr, "validate: dcodec not supported (auto-detected)\n"); ok = 0;
    }
    if (xc->gpu_index) {
        fprintf(stderr, "validate: gpu_index not supported\n"); ok = 0;
    }
    if (xc->crypt_scheme) {
        fprintf(stderr, "validate: encryption not supported\n"); ok = 0;
    }
    if (xc->watermark_text && xc->watermark_text[0]) {
        fprintf(stderr, "validate: watermark not supported\n"); ok = 0;
    }
    if (xc->filter_descriptor) {
        fprintf(stderr, "validate: filter_descriptor not supported\n"); ok = 0;
    }
    if (xc->n_audio) {
        fprintf(stderr, "validate: audio streams not supported\n"); ok = 0;
    }
    if (xc->deinterlace) {
        fprintf(stderr, "validate: deinterlace not supported\n"); ok = 0;
    }
    if (xc->rotate) {
        fprintf(stderr, "validate: rotate not supported\n"); ok = 0;
    }

    if (xc->bitdepth && xc->bitdepth != 8 && xc->bitdepth != 10) {
        fprintf(stderr, "validate: bitdepth must be 8 or 10 (got %d)\n", xc->bitdepth);
        ok = 0;
    }

    return ok ? 0 : -1;
}

/* ------------------------------------------------------------------ */
/* Internal: open and prepare one input file for decoding              */
/* ------------------------------------------------------------------ */
static int open_input(mvhevc_input *inp, const char *filename, int out_w, int out_h)
{
    int ret;

    memset(inp, 0, sizeof(*inp));
    inp->video_idx = -1;

    ret = avformat_open_input(&inp->fmt_ctx, filename, NULL, NULL);
    if (ret < 0) {
        fprintf(stderr, "Cannot open input '%s': %s\n", filename, av_err2str(ret));
        return ret;
    }

    ret = avformat_find_stream_info(inp->fmt_ctx, NULL);
    if (ret < 0) {
        fprintf(stderr, "Cannot find stream info in '%s': %s\n", filename, av_err2str(ret));
        return ret;
    }

    ret = av_find_best_stream(inp->fmt_ctx, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (ret < 0) {
        fprintf(stderr, "No video stream found in '%s'\n", filename);
        return ret;
    }
    inp->video_idx = ret;

    AVStream *st = inp->fmt_ctx->streams[inp->video_idx];
    const AVCodec *dec = avcodec_find_decoder(st->codecpar->codec_id);
    if (!dec) {
        fprintf(stderr, "No decoder for codec %d\n", st->codecpar->codec_id);
        return AVERROR_DECODER_NOT_FOUND;
    }

    inp->dec_ctx = avcodec_alloc_context3(dec);
    if (!inp->dec_ctx)
        return AVERROR(ENOMEM);

    ret = avcodec_parameters_to_context(inp->dec_ctx, st->codecpar);
    if (ret < 0) return ret;

    inp->dec_ctx->pkt_timebase = st->time_base;

    ret = avcodec_open2(inp->dec_ctx, dec, NULL);
    if (ret < 0) {
        fprintf(stderr, "Cannot open decoder for '%s': %s\n", filename, av_err2str(ret));
        return ret;
    }

    inp->width  = (out_w > 0) ? out_w : inp->dec_ctx->width;
    inp->height = (out_h > 0) ? out_h : inp->dec_ctx->height;

    inp->frame = av_frame_alloc();
    inp->frame420 = av_frame_alloc();
    inp->pkt = av_packet_alloc();
    if (!inp->frame || !inp->frame420 || !inp->pkt)
        return AVERROR(ENOMEM);

    inp->frame420->format = AV_PIX_FMT_YUV420P;
    inp->frame420->width  = inp->width;
    inp->frame420->height = inp->height;
    ret = av_frame_get_buffer(inp->frame420, 0);
    if (ret < 0) return ret;

    inp->sws_ctx = sws_getContext(
        inp->dec_ctx->width, inp->dec_ctx->height, inp->dec_ctx->pix_fmt,
        inp->width, inp->height, AV_PIX_FMT_YUV420P,
        SWS_BICUBIC, NULL, NULL, NULL);
    if (!inp->sws_ctx) {
        fprintf(stderr, "Cannot create sws context\n");
        return AVERROR(ENOMEM);
    }

    return 0;
}

/* ------------------------------------------------------------------ */
/* Internal: open a single MV-HEVC file, decoding all views            */
/* ------------------------------------------------------------------ */
static int open_input_mvhevc(mvhevc_input *inp, const char *filename, int out_w, int out_h)
{
    int ret;

    memset(inp, 0, sizeof(*inp));
    inp->video_idx = -1;

    ret = avformat_open_input(&inp->fmt_ctx, filename, NULL, NULL);
    if (ret < 0) {
        fprintf(stderr, "Cannot open input '%s': %s\n", filename, av_err2str(ret));
        return ret;
    }

    ret = avformat_find_stream_info(inp->fmt_ctx, NULL);
    if (ret < 0) {
        fprintf(stderr, "Cannot find stream info in '%s': %s\n", filename, av_err2str(ret));
        return ret;
    }

    ret = av_find_best_stream(inp->fmt_ctx, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (ret < 0) {
        fprintf(stderr, "No video stream found in '%s'\n", filename);
        return ret;
    }
    inp->video_idx = ret;

    AVStream *st = inp->fmt_ctx->streams[inp->video_idx];
    const AVCodec *dec = avcodec_find_decoder(st->codecpar->codec_id);
    if (!dec) {
        fprintf(stderr, "No decoder for codec %d\n", st->codecpar->codec_id);
        return AVERROR_DECODER_NOT_FOUND;
    }

    inp->dec_ctx = avcodec_alloc_context3(dec);
    if (!inp->dec_ctx)
        return AVERROR(ENOMEM);

    ret = avcodec_parameters_to_context(inp->dec_ctx, st->codecpar);
    if (ret < 0) return ret;

    inp->dec_ctx->pkt_timebase = st->time_base;

    /* Request all views from MV-HEVC decoder.
     * view_ids is an array option; a single -1 decodes all views. */
    int64_t view_all = -1;
    av_opt_set_int(inp->dec_ctx->priv_data, "view_ids", view_all, AV_OPT_SEARCH_CHILDREN);

    ret = avcodec_open2(inp->dec_ctx, dec, NULL);
    if (ret < 0) {
        fprintf(stderr, "Cannot open decoder for '%s': %s\n", filename, av_err2str(ret));
        return ret;
    }

    inp->width  = (out_w > 0) ? out_w : inp->dec_ctx->width;
    inp->height = (out_h > 0) ? out_h : inp->dec_ctx->height;

    inp->frame = av_frame_alloc();
    inp->frame420 = av_frame_alloc();
    inp->pkt = av_packet_alloc();
    if (!inp->frame || !inp->frame420 || !inp->pkt)
        return AVERROR(ENOMEM);

    inp->frame420->format = AV_PIX_FMT_YUV420P;
    inp->frame420->width  = inp->width;
    inp->frame420->height = inp->height;
    ret = av_frame_get_buffer(inp->frame420, 0);
    if (ret < 0) return ret;

    /* sws context will be created on first frame (input pix_fmt may vary) */
    inp->sws_ctx = sws_getContext(
        inp->dec_ctx->width, inp->dec_ctx->height, inp->dec_ctx->pix_fmt,
        inp->width, inp->height, AV_PIX_FMT_YUV420P,
        SWS_BICUBIC, NULL, NULL, NULL);
    if (!inp->sws_ctx) {
        fprintf(stderr, "Cannot create sws context\n");
        return AVERROR(ENOMEM);
    }

    return 0;
}

/* ------------------------------------------------------------------ */
/* Internal: decode next video frame                                   */
/* ------------------------------------------------------------------ */
static int decode_next_frame(mvhevc_input *inp)
{
    int ret;

    if (inp->eof)
        return AVERROR_EOF;

    for (;;) {
        ret = avcodec_receive_frame(inp->dec_ctx, inp->frame);
        if (ret == 0) {
            av_frame_make_writable(inp->frame420);
            sws_scale(inp->sws_ctx,
                      (const uint8_t *const *)inp->frame->data,
                      inp->frame->linesize, 0, inp->dec_ctx->height,
                      inp->frame420->data, inp->frame420->linesize);
            return 0;
        }
        if (ret == AVERROR_EOF) {
            inp->eof = 1;
            return AVERROR_EOF;
        }
        if (ret != AVERROR(EAGAIN))
            return ret;

        for (;;) {
            ret = av_read_frame(inp->fmt_ctx, inp->pkt);
            if (ret < 0) {
                avcodec_send_packet(inp->dec_ctx, NULL);
                break;
            }
            if (inp->pkt->stream_index == inp->video_idx) {
                ret = avcodec_send_packet(inp->dec_ctx, inp->pkt);
                av_packet_unref(inp->pkt);
                if (ret < 0 && ret != AVERROR(EAGAIN))
                    return ret;
                break;
            }
            av_packet_unref(inp->pkt);
        }
    }
}

/* ------------------------------------------------------------------ */
/* Internal: close one input                                           */
/* ------------------------------------------------------------------ */
static void close_input(mvhevc_input *inp)
{
    sws_freeContext(inp->sws_ctx);
    inp->sws_ctx = NULL;
    av_frame_free(&inp->frame);
    av_frame_free(&inp->frame420);
    av_packet_free(&inp->pkt);
    avcodec_free_context(&inp->dec_ctx);
    avformat_close_input(&inp->fmt_ctx);
}

/* ------------------------------------------------------------------ */
/* Internal: fill x265_picture from decoded AVFrame                    */
/* ------------------------------------------------------------------ */
static void fill_x265_pic(x265_picture *pic, AVFrame *f, int64_t pts,
                           int keyint, int force_idr)
{
    pic->planes[0] = f->data[0];
    pic->planes[1] = f->data[1];
    pic->planes[2] = f->data[2];
    pic->stride[0] = f->linesize[0];
    pic->stride[1] = f->linesize[1];
    pic->stride[2] = f->linesize[2];
    pic->pts       = pts;
    pic->bitDepth  = 8;
    pic->sliceType = (force_idr || (keyint > 0 && pts % keyint == 0))
                     ? X265_TYPE_IDR : X265_TYPE_AUTO;
}

/* ------------------------------------------------------------------ */
/* Internal: write NAL units to output file                            */
/* ------------------------------------------------------------------ */
static int write_nals(FILE *fp, x265_nal *nals, uint32_t nnal)
{
    for (uint32_t i = 0; i < nnal; i++) {
        if (fwrite(nals[i].payload, 1, nals[i].sizeBytes, fp) != (size_t)nals[i].sizeBytes) {
            fprintf(stderr, "Error writing NAL unit\n");
            return -1;
        }
    }
    return 0;
}

/* ------------------------------------------------------------------ */
/* Internal: configure x265 encoder from params                        */
/* ------------------------------------------------------------------ */
static int setup_encoder(mvhevc_ctx *c)
{
    xcparams_t *xc = c->params;
    mvhevc_params *mv = &c->mv;
    int bitdepth = xc->bitdepth ? xc->bitdepth : 8;

    c->api = x265_api_get(bitdepth);
    if (!c->api) {
        fprintf(stderr, "Cannot get x265 API for %d-bit\n", bitdepth);
        return -1;
    }

    c->x265_params = c->api->param_alloc();
    if (!c->x265_params) {
        fprintf(stderr, "Cannot allocate x265 params\n");
        return -1;
    }

    x265_param *param = c->x265_params;
    const x265_api *api = c->api;

    /* Preset / tune */
    api->param_default_preset(param, xc->preset ? xc->preset : "medium", mv->tune);

    /* Basic encoding parameters */
    param->sourceWidth    = c->left.width;
    param->sourceHeight   = c->left.height;
    param->fpsNum         = c->fps_num;
    param->fpsDenom       = c->fps_den;
    param->internalCsp    = X265_CSP_I420;
    param->bRepeatHeaders = 1;

    /* GOP / keyframe control */
    if (xc->force_keyint > 0)
        api->param_parse(param, "keyint", "9999");
    {
        char buf[32];
        snprintf(buf, sizeof(buf), "%d", mv->scenecut);
        api->param_parse(param, "scenecut", buf);
    }
    if (mv->bframes >= 0) {
        char buf[16];
        snprintf(buf, sizeof(buf), "%d", mv->bframes);
        api->param_parse(param, "bframes", buf);
    }

    /* Rate control */
    if (xc->video_bitrate > 0) {
        char buf[32];
        snprintf(buf, sizeof(buf), "%d", xc->video_bitrate);
        api->param_parse(param, "bitrate", buf);
    } else {
        api->param_parse(param, "crf", xc->crf_str ? xc->crf_str : "23");
    }
    if (xc->rc_max_rate > 0) {
        char buf[32];
        snprintf(buf, sizeof(buf), "%d", xc->rc_max_rate);
        api->param_parse(param, "vbv-maxrate", buf);
    }
    if (xc->rc_buffer_size > 0) {
        char buf[32];
        snprintf(buf, sizeof(buf), "%d", xc->rc_buffer_size);
        api->param_parse(param, "vbv-bufsize", buf);
    }

    /* Level and tier */
    if (xc->level > 0) {
        param->levelIdc  = xc->level;
        param->bHighTier = mv->high_tier;
    }

    /* Enable multiview */
    if (api->param_parse(param, "num-views", "2") != 0) {
        fprintf(stderr, "Error: x265 does not support num-views parameter.\n"
                "Make sure x265 was built with ENABLE_MULTIVIEW=1\n");
        return -1;
    }
    api->param_parse(param, "format", "0");
    param->numLayers = param->numViews;

    /* Profile */
    if (xc->profile) {
        if (api->param_apply_profile(param, xc->profile) < 0) {
            fprintf(stderr, "Invalid profile: %s\n", xc->profile);
            return -1;
        }
    }

    /* HDR metadata */
    if (xc->max_cll)
        api->param_parse(param, "max-cll", xc->max_cll);
    if (xc->master_display)
        api->param_parse(param, "master-display", xc->master_display);

    /* Color space from input */
    {
        AVCodecParameters *cp = c->left.fmt_ctx->streams[c->left.video_idx]->codecpar;
        if (cp->color_primaries != AVCOL_PRI_UNSPECIFIED) {
            param->vui.bEnableVideoSignalTypePresentFlag = 1;
            param->vui.bEnableColorDescriptionPresentFlag = 1;
            param->vui.colorPrimaries          = cp->color_primaries;
            param->vui.transferCharacteristics = cp->color_trc;
            param->vui.matrixCoeffs            = cp->color_space;
        }
        if (cp->color_range == AVCOL_RANGE_JPEG)
            param->vui.bEnableVideoFullRangeFlag = 1;
    }

    /* Open encoder */
    c->encoder = api->encoder_open(param);
    if (!c->encoder) {
        fprintf(stderr, "Cannot open x265 encoder\n");
        return -1;
    }

    return 0;
}

/* ================================================================== */
/* Public API                                                          */
/* ================================================================== */

/* ------------------------------------------------------------------ */
/* mvhevc_init                                                         */
/* ------------------------------------------------------------------ */
int mvhevc_init(mvhevc_ctx **ctx,
                xcparams_t *xc,
                mvhevc_params *mv,
                const char *left_file,
                const char *right_file,
                const char *out_file)
{
    int ret;

    /* Validate parameters */
    if (mvhevc_validate_params(xc) < 0)
        return -1;

    /* Allocate context */
    mvhevc_ctx *c = calloc(1, sizeof(mvhevc_ctx));
    if (!c)
        return -1;
    *ctx = c;

    c->params = xc;
    c->mv = *mv;
    c->single_input = (right_file == NULL);

    /* Auto-compute width from height if only height is specified.
     * Probe input dimensions, preserve aspect ratio, round to even. */
    if (xc->enc_height > 0 && xc->enc_width == 0) {
        AVFormatContext *probe = NULL;
        ret = avformat_open_input(&probe, left_file, NULL, NULL);
        if (ret < 0) {
            fprintf(stderr, "Cannot probe input '%s': %s\n", left_file, av_err2str(ret));
            return ret;
        }
        avformat_find_stream_info(probe, NULL);
        int vi = av_find_best_stream(probe, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
        if (vi >= 0) {
            int in_w = probe->streams[vi]->codecpar->width;
            int in_h = probe->streams[vi]->codecpar->height;
            xc->enc_width = (in_w * xc->enc_height / in_h + 1) & ~1;
            fprintf(stderr, "Auto width: %dx%d -> %dx%d\n",
                    in_w, in_h, xc->enc_width, xc->enc_height);
        }
        avformat_close_input(&probe);
    }

    if (c->single_input) {
        /* Single MV-HEVC input: decode both views from one file */
        fprintf(stderr, "Opening MV-HEVC input: %s\n", left_file);
        ret = open_input_mvhevc(&c->left, left_file, xc->enc_width, xc->enc_height);
        if (ret < 0) return ret;

        /* Allocate a second frame420 for the right eye view */
        c->right_frame420 = av_frame_alloc();
        if (!c->right_frame420) return AVERROR(ENOMEM);
        c->right_frame420->format = AV_PIX_FMT_YUV420P;
        c->right_frame420->width  = c->left.width;
        c->right_frame420->height = c->left.height;
        ret = av_frame_get_buffer(c->right_frame420, 0);
        if (ret < 0) return ret;
    } else {
        /* Two separate input files */
        fprintf(stderr, "Opening left eye: %s\n", left_file);
        ret = open_input(&c->left, left_file, xc->enc_width, xc->enc_height);
        if (ret < 0) return ret;

        fprintf(stderr, "Opening right eye: %s\n", right_file);
        ret = open_input(&c->right, right_file, xc->enc_width, xc->enc_height);
        if (ret < 0) return ret;

        if (c->left.width != c->right.width || c->left.height != c->right.height) {
            fprintf(stderr, "Error: left (%dx%d) and right (%dx%d) dimensions must match\n",
                    c->left.width, c->left.height, c->right.width, c->right.height);
            return -1;
        }
    }

    if (xc->enc_width > 0)
        fprintf(stderr, "Output resolution: %dx%d (rescaled)\n", c->left.width, c->left.height);
    else
        fprintf(stderr, "Input dimensions: %dx%d\n", c->left.width, c->left.height);

    /* Determine framerate */
    if (mv->fps_num > 0 && mv->fps_den > 0) {
        c->fps_num = mv->fps_num;
        c->fps_den = mv->fps_den;
    } else {
        AVStream *lst = c->left.fmt_ctx->streams[c->left.video_idx];
        c->fps_num = lst->avg_frame_rate.num;
        c->fps_den = lst->avg_frame_rate.den;
        if (c->fps_num <= 0 || c->fps_den <= 0) {
            c->fps_num = 30;
            c->fps_den = 1;
        }
    }
    fprintf(stderr, "Framerate: %d/%d (%.2f fps)\n",
            c->fps_num, c->fps_den, (double)c->fps_num / c->fps_den);

    /* Set up x265 encoder */
    ret = setup_encoder(c);
    if (ret < 0) return ret;

    /* Log configuration */
    fprintf(stderr, "\nx265 multiview encoder configuration:\n");
    fprintf(stderr, "  Resolution:  %dx%d\n", c->left.width, c->left.height);
    fprintf(stderr, "  Framerate:   %d/%d\n", c->fps_num, c->fps_den);
    if (xc->video_bitrate > 0)
        fprintf(stderr, "  Bitrate:     %d kbps\n", xc->video_bitrate);
    else
        fprintf(stderr, "  CRF:         %s\n", xc->crf_str ? xc->crf_str : "23");
    if (xc->force_keyint > 0)
        fprintf(stderr, "  Keyint:      %d (forced IDR)\n", xc->force_keyint);
    if (xc->profile)
        fprintf(stderr, "  Profile:     %s\n", xc->profile);
    if (xc->level > 0)
        fprintf(stderr, "  Level:       %.1f %s\n", xc->level / 10.0,
                mv->high_tier ? "(High tier)" : "(Main tier)");
    fprintf(stderr, "  Views:       2 (MV-HEVC)\n\n");

    /* Open output file */
    c->fp_out = fopen(out_file, "wb");
    if (!c->fp_out) {
        fprintf(stderr, "Cannot open output file '%s'\n", out_file);
        return -1;
    }

    /* Write headers (VPS/SPS/PPS) */
    {
        x265_nal *nals;
        uint32_t nnal;
        ret = c->api->encoder_headers(c->encoder, &nals, &nnal);
        if (ret < 0) {
            fprintf(stderr, "Cannot get encoder headers\n");
            return ret;
        }
        if (write_nals(c->fp_out, nals, nnal) < 0)
            return -1;
        fprintf(stderr, "Wrote %u header NAL units\n", nnal);
    }

    c->frame_count = 0;
    return 0;
}

/* ------------------------------------------------------------------ */
/* mvhevc_xc                                                           */
/* ------------------------------------------------------------------ */
int mvhevc_xc(mvhevc_ctx *c)
{
    int ret = 0;
    int encoding = 1;
    int force_keyint = c->params->force_keyint;

    while (encoding) {
        x265_picture pics_in[2];
        x265_picture pics_out[2];
        x265_nal *nals = NULL;
        uint32_t nnal = 0;
        int have_frames = 1;

        c->api->picture_init(c->x265_params, &pics_in[0]);
        c->api->picture_init(c->x265_params, &pics_in[1]);

        /* Decode frames for both views */
        if (c->single_input) {
            /* Single MV-HEVC: decoder outputs view 0 then view 1 alternating.
             * decode_next_frame writes into left.frame420, so we must copy
             * view 0 (left) before decoding view 1 (right) which overwrites it. */
            int ret_v = decode_next_frame(&c->left);
            if (ret_v == AVERROR_EOF) {
                have_frames = 0;
            } else if (ret_v < 0) {
                fprintf(stderr, "Error decoding view 0 frame %lld: %s\n",
                        (long long)c->frame_count, av_err2str(ret_v));
                return ret_v;
            }

            if (have_frames) {
                /* Copy view 0 (left eye) to right_frame420 temporarily,
                 * then decode view 1 into left.frame420, then swap pointers. */
                av_frame_copy(c->right_frame420, c->left.frame420);

                ret_v = decode_next_frame(&c->left);
                if (ret_v == AVERROR_EOF) {
                    have_frames = 0;
                } else if (ret_v < 0) {
                    fprintf(stderr, "Error decoding view 1 frame %lld: %s\n",
                            (long long)c->frame_count, av_err2str(ret_v));
                    return ret_v;
                }
            }

            if (have_frames) {
                /* Now: left.frame420 = view 1 (right eye)
                 *      right_frame420 = view 0 (left eye)
                 * Encode: view 0 from right_frame420, view 1 from left.frame420 */
                fill_x265_pic(&pics_in[0], c->right_frame420, c->frame_count,
                              force_keyint, c->frame_count == 0);
                fill_x265_pic(&pics_in[1], c->left.frame420, c->frame_count,
                              force_keyint, c->frame_count == 0);
            }
        } else {
            int ret_l = decode_next_frame(&c->left);
            int ret_r = decode_next_frame(&c->right);

            if (ret_l == AVERROR_EOF || ret_r == AVERROR_EOF) {
                have_frames = 0;
            } else if (ret_l < 0) {
                fprintf(stderr, "Error decoding left eye frame %lld: %s\n",
                        (long long)c->frame_count, av_err2str(ret_l));
                return ret_l;
            } else if (ret_r < 0) {
                fprintf(stderr, "Error decoding right eye frame %lld: %s\n",
                        (long long)c->frame_count, av_err2str(ret_r));
                return ret_r;
            }

            if (have_frames) {
                fill_x265_pic(&pics_in[0], c->left.frame420, c->frame_count,
                              force_keyint, c->frame_count == 0);
                fill_x265_pic(&pics_in[1], c->right.frame420, c->frame_count,
                              force_keyint, c->frame_count == 0);
            }
        }

        memset(pics_out, 0, sizeof(pics_out));
        int enc_ret = c->api->encoder_encode(
            c->encoder, &nals, &nnal,
            have_frames ? &pics_in[0] : NULL,
            pics_out);

        if (enc_ret < 0) {
            fprintf(stderr, "Encode error at frame %lld\n", (long long)c->frame_count);
            return -1;
        }

        if (nnal > 0) {
            if (write_nals(c->fp_out, nals, nnal) < 0)
                return -1;
        }

        if (have_frames) {
            c->frame_count++;
            if (c->frame_count % 100 == 0)
                fprintf(stderr, "Encoded %lld frames...\n", (long long)c->frame_count);
        } else {
            if (nnal == 0 && enc_ret == 0)
                encoding = 0;
        }
    }

    /* Flush remaining frames */
    for (;;) {
        x265_nal *nals = NULL;
        uint32_t nnal = 0;
        x265_picture pics_out[2];
        memset(pics_out, 0, sizeof(pics_out));

        int enc_ret = c->api->encoder_encode(c->encoder, &nals, &nnal, NULL, pics_out);
        if (enc_ret < 0) break;
        if (nnal > 0)
            write_nals(c->fp_out, nals, nnal);
        if (nnal == 0 && enc_ret == 0)
            break;
    }

    fprintf(stderr, "\nEncoding complete: %lld frames\n", (long long)c->frame_count);
    return ret;
}

/* ------------------------------------------------------------------ */
/* mvhevc_fini                                                         */
/* ------------------------------------------------------------------ */
void mvhevc_fini(mvhevc_ctx **ctx)
{
    if (!ctx || !*ctx)
        return;

    mvhevc_ctx *c = *ctx;

    if (c->fp_out)
        fclose(c->fp_out);
    if (c->encoder && c->api)
        c->api->encoder_close(c->encoder);
    if (c->x265_params && c->api)
        c->api->param_free(c->x265_params);

    close_input(&c->left);
    close_input(&c->right);

    free(c);
    *ctx = NULL;
}
