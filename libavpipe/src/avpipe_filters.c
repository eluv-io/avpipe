/*
 * avpipe_filters.c
 */

#include "avpipe_xc.h"
#include "elv_log.h"

int
init_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params)
{
    AVCodecContext *dec_codec_ctx = decoder_context->codec_context[decoder_context->video_stream_index];

    char args[512];
    int ret = 0;
    const AVFilter *buffersrc  = avfilter_get_by_name("buffer");
    const AVFilter *buffersink = avfilter_get_by_name("buffersink");
    AVFilterInOut *outputs = avfilter_inout_alloc();
    AVFilterInOut *inputs  = avfilter_inout_alloc();
    AVRational time_base = decoder_context->format_context->streams[decoder_context->video_stream_index]->time_base;
    enum AVPixelFormat pix_fmts[] = { AV_PIX_FMT_YUV422P /* AV_PIX_FMT_GRAY8 */, AV_PIX_FMT_NONE };

    /* If the codec is nvenc, replace AV_PIX_FMT_YUV422P with AV_PIX_FMT_YUV420P */
    if (params->codec && !strcmp(params->codec, "h264_nvenc")) {
        pix_fmts[0] = AV_PIX_FMT_YUV420P;
        elv_warn("Replace pixel format to AV_PIX_FMT_YUV420P for campatibility with NVENC codec");
    }

    decoder_context->filter_graph = avfilter_graph_alloc();
    if (!outputs || !inputs || !decoder_context->filter_graph) {
        ret = AVERROR(ENOMEM);
        goto end;
    }

    /* buffer video source: the decoded frames from the decoder will be inserted here. */
    snprintf(args, sizeof(args),
        "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
        dec_codec_ctx->width, dec_codec_ctx->height, dec_codec_ctx->pix_fmt,
        time_base.num, time_base.den,
        dec_codec_ctx->sample_aspect_ratio.num, dec_codec_ctx->sample_aspect_ratio.den);

    ret = avfilter_graph_create_filter(&decoder_context->buffersrc_ctx, buffersrc, "in",
                                       args, NULL, decoder_context->filter_graph);
    if (ret < 0) {
        av_log(NULL, AV_LOG_ERROR, "Cannot create buffer source\n");
        goto end;
    }

    /* buffer video sink: to terminate the filter chain. */
    ret = avfilter_graph_create_filter(&decoder_context->buffersink_ctx, buffersink, "out",
                                       NULL, NULL, decoder_context->filter_graph);
    if (ret < 0) {
        av_log(NULL, AV_LOG_ERROR, "Cannot create buffer sink\n");
        goto end;
    }

    ret = av_opt_set_int_list(decoder_context->buffersink_ctx, "pix_fmts", pix_fmts,
                              AV_PIX_FMT_NONE, AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        av_log(NULL, AV_LOG_ERROR, "Cannot set output pixel format\n");
        goto end;
    }

    /*
     * Set the endpoints for the filter graph. The filter_graph will
     * be linked to the graph described by filters_descr.
     */

    /*
     * The buffer source output must be connected to the input pad of
     * the first filter described by filters_descr; since the first
     * filter input label is not specified, it is set to "in" by
     * default.
     */
    outputs->name       = av_strdup("in");
    outputs->filter_ctx = decoder_context->buffersrc_ctx;
    outputs->pad_idx    = 0;
    outputs->next       = NULL;

    /*
     * The buffer sink input must be connected to the output pad of
     * the last filter described by filters_descr; since the last
     * filter output label is not specified, it is set to "out" by
     * default.
     */
    inputs->name       = av_strdup("out");
    inputs->filter_ctx = decoder_context->buffersink_ctx;
    inputs->pad_idx    = 0;
    inputs->next       = NULL;

    if ((ret = avfilter_graph_parse_ptr(decoder_context->filter_graph, filters_descr,
                                    &inputs, &outputs, NULL)) < 0)
        goto end;

    if ((ret = avfilter_graph_config(decoder_context->filter_graph, NULL)) < 0)
        goto end;

end:
    avfilter_inout_free(&inputs);
    avfilter_inout_free(&outputs);

    return ret;
}
