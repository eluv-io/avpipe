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
    AVRational time_base;
    enum AVPixelFormat pix_fmts[] = { AV_PIX_FMT_YUV422P /* AV_PIX_FMT_GRAY8 */, AV_PIX_FMT_NONE };

    /* If there is no video stream, then return */
    if (decoder_context->video_stream_index < 0)
        return 0;

    time_base = decoder_context->format_context->streams[decoder_context->video_stream_index]->time_base;

    decoder_context->video_filter_graph = avfilter_graph_alloc();
    if (!outputs || !inputs || !decoder_context->video_filter_graph) {
        ret = AVERROR(ENOMEM);
        goto end;
    }

    /* buffer video source: the decoded frames from the decoder will be inserted here. */
    snprintf(args, sizeof(args),
        "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
        dec_codec_ctx->width, dec_codec_ctx->height, dec_codec_ctx->pix_fmt,
        time_base.num, time_base.den,
        dec_codec_ctx->sample_aspect_ratio.num, dec_codec_ctx->sample_aspect_ratio.den);

    /* video_stream_index should be the same in both encoder and decoder context */
    pix_fmts[0] = encoder_context->codec_context[decoder_context->video_stream_index]->pix_fmt;

    ret = avfilter_graph_create_filter(&decoder_context->video_buffersrc_ctx, buffersrc, "in",
                                       args, NULL, decoder_context->video_filter_graph);
    if (ret < 0) {
        av_log(NULL, AV_LOG_ERROR, "Cannot create buffer source err=%d\n", ret);
        goto end;
    }

    /* buffer video sink: to terminate the filter chain. */
    ret = avfilter_graph_create_filter(&decoder_context->video_buffersink_ctx, buffersink, "out",
                                       NULL, NULL, decoder_context->video_filter_graph);
    if (ret < 0) {
        av_log(NULL, AV_LOG_ERROR, "Cannot create buffer sink\n");
        goto end;
    }

    ret = av_opt_set_int_list(decoder_context->video_buffersink_ctx, "pix_fmts", pix_fmts,
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
    outputs->filter_ctx = decoder_context->video_buffersrc_ctx;
    outputs->pad_idx    = 0;
    outputs->next       = NULL;

    /*
     * The buffer sink input must be connected to the output pad of
     * the last filter described by filters_descr; since the last
     * filter output label is not specified, it is set to "out" by
     * default.
     */
    inputs->name       = av_strdup("out");
    inputs->filter_ctx = decoder_context->video_buffersink_ctx;
    inputs->pad_idx    = 0;
    inputs->next       = NULL;

    if ((ret = avfilter_graph_parse_ptr(decoder_context->video_filter_graph, filters_descr,
                                    &inputs, &outputs, NULL)) < 0)
        goto end;

    if ((ret = avfilter_graph_config(decoder_context->video_filter_graph, NULL)) < 0)
        goto end;

end:
    avfilter_inout_free(&inputs);
    avfilter_inout_free(&outputs);

    return ret;
}

int
init_audio_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params)
{
    if (decoder_context->audio_stream_index < 0){
        return -1;  //REVIEW want some unique val to say no audio present
    }
    AVCodecContext *dec_codec_ctx = decoder_context->codec_context[decoder_context->audio_stream_index[0]];
    AVCodecContext *enc_codec_ctx = encoder_context->codec_context[encoder_context->audio_stream_index[0]];
    char args[512];
    int ret = 0;
    AVFilterContext **abuffersrc_ctx = NULL;
    AVFilterContext *buffersink_ctx = NULL;
    AVFilterContext *format_ctx = NULL;
    const AVFilter *buffersrc = avfilter_get_by_name("abuffer");
    const AVFilter *buffersink = avfilter_get_by_name("abuffersink");
    const AVFilter *aformat = avfilter_get_by_name("aformat");
    AVFilterGraph *filter_graph;

    if (!dec_codec_ctx) {
        elv_err("Audio decoder was not initialized!");
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    filter_graph = avfilter_graph_alloc();
    if (!buffersrc || !buffersink || !filter_graph) {
        elv_err("Audio filtering source or sink element not found");
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    if (!dec_codec_ctx->channel_layout)
        dec_codec_ctx->channel_layout = av_get_default_channel_layout(dec_codec_ctx->channels);

    snprintf(args, sizeof(args),
        "time_base=%d/%d:sample_rate=%d:sample_fmt=%s:channel_layout=0x%"PRIx64,
        1, dec_codec_ctx->sample_rate, dec_codec_ctx->sample_rate,
        av_get_sample_fmt_name(dec_codec_ctx->sample_fmt),
        dec_codec_ctx->channel_layout);
    elv_log("Audio srcfilter args=%s", args);

    /* decoder_context->n_audio is 1 */
    abuffersrc_ctx = (AVFilterContext **) calloc(1, sizeof(AVFilterContext *));
    if (!abuffersrc_ctx) {
        elv_err("Failed to allocate memory for audio buffersrc filter");
        ret = AVERROR(ENOMEM);
        goto end;
    }

    ret = avfilter_graph_create_filter(&abuffersrc_ctx[0], buffersrc, "in", args, NULL, filter_graph);
    if (ret < 0) {
        elv_err("Cannot create audio buffer source");
        goto end;
    }

    ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out", NULL, NULL, filter_graph);
    if (ret < 0) {
        elv_err("Cannot create audio buffer sink");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_fmts",
        (uint8_t*)&enc_codec_ctx->sample_fmt, sizeof(enc_codec_ctx->sample_fmt),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("Cannot set output sample format");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_rates",
        (uint8_t*)&enc_codec_ctx->sample_rate, sizeof(enc_codec_ctx->sample_rate),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("Cannot set output sample rate");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "channel_layouts",
        (uint8_t*)&enc_codec_ctx->channel_layout,
        sizeof(enc_codec_ctx->channel_layout), AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("Cannot set output channel layout");
        goto end;
    }

    snprintf(args, sizeof(args),
             "sample_fmts=%s:sample_rates=%d:channel_layouts=0x%"PRIx64,
             av_get_sample_fmt_name(enc_codec_ctx->sample_fmt), enc_codec_ctx->sample_rate,
             (uint64_t)enc_codec_ctx->channel_layout);
    elv_log("Audio format_filter args=%s", args);

    ret = avfilter_graph_create_filter(&format_ctx, aformat, "format_out_0_0", args, NULL, filter_graph);
    if (ret < 0) {
        elv_err("Cannot create audio format filter");
        goto end;
    }

    if ((ret = avfilter_link(abuffersrc_ctx[0], 0, format_ctx, 0)) < 0) {
        elv_err("Failed to link audio src to format, ret=%d", ret);
        return ret;
    }

    if ((ret = avfilter_link(format_ctx, 0, buffersink_ctx, 0)) < 0) {
        elv_err("Failed to link audio format to sink, ret=%d", ret);
        return ret;
    }

    av_buffersink_set_frame_size(buffersink_ctx,
        encoder_context->codec_context[decoder_context->audio_stream_index[0]]->frame_size);

    if ((ret = avfilter_graph_config(filter_graph, NULL)) < 0)
        goto end;

    /* Fill FilteringContext */
    decoder_context->audio_filter_graph = filter_graph;
    decoder_context->audio_buffersrc_ctx = abuffersrc_ctx;
    decoder_context->audio_buffersink_ctx = buffersink_ctx;

end:

    return ret;

}

int
init_audio_join_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params)
{
    if (decoder_context->audio_stream_index < 0){
        return -1;  //REVIEW want some unique val to say no audio present
    }
    AVCodecContext *enc_codec_ctx = encoder_context->codec_context[encoder_context->audio_stream_index[0]];
    char args[512];
    char format_args[512];
    int ret = 0;
    AVFilterContext **abuffersrc_ctx = NULL;
    AVFilterContext *buffersink_ctx = NULL;
    AVFilterContext *format_ctx = NULL;
    AVFilterContext *join_ctx = NULL;
    const AVFilter *buffersrc = avfilter_get_by_name("abuffer");
    const AVFilter *buffersink = avfilter_get_by_name("abuffersink");
    const AVFilter *aformat = avfilter_get_by_name("aformat");
    const AVFilter *join = avfilter_get_by_name("join");

    decoder_context->audio_filter_graph = avfilter_graph_alloc();
    if (!buffersrc || !buffersink || !join || !decoder_context->audio_filter_graph) {
        elv_err("Audio filtering source/sink/join filter not found");
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    abuffersrc_ctx = (AVFilterContext **) calloc(decoder_context->n_audio, sizeof(AVFilterContext *));
    if (!abuffersrc_ctx) {
        elv_err("Failed to allocate memory for audio join buffersrc filter");
        ret = AVERROR(ENOMEM);
        goto end;
    }

    /* Create join filter with n inputs */
    sprintf(args, "inputs=%d", decoder_context->n_audio);
    ret = avfilter_graph_create_filter(&join_ctx, join, "join", args, NULL, decoder_context->audio_filter_graph);
    if (ret < 0) {
        elv_err("Cannot create audio join");
        goto end;
    }

    /* For each audio input create an audio source filter and link it to join filter */
    for (int i=0; i<decoder_context->n_audio; i++) {
        AVCodecContext *dec_codec_ctx = decoder_context->codec_context[decoder_context->audio_stream_index[i]];
        char filt_name[32];

        if (!dec_codec_ctx) {
            elv_err("Audio join decoder was not initialized!");
            ret = AVERROR_UNKNOWN;
            goto end;
        }

        if (!dec_codec_ctx->channel_layout)
            dec_codec_ctx->channel_layout = av_get_default_channel_layout(dec_codec_ctx->channels);

        snprintf(args, sizeof(args),
            "time_base=%d/%d:sample_rate=%d:sample_fmt=%s:channel_layout=0x%"PRIx64,
            1, dec_codec_ctx->sample_rate, dec_codec_ctx->sample_rate,
            av_get_sample_fmt_name(dec_codec_ctx->sample_fmt),
            dec_codec_ctx->channel_layout);
            //av_get_channel_layout("BL"));

        sprintf(filt_name, "in_%d", i);
        elv_log("Audio srcfilter=%s args=%s", filt_name, args);

        ret = avfilter_graph_create_filter(&abuffersrc_ctx[i], buffersrc, filt_name, args, NULL, decoder_context->audio_filter_graph);
        if (ret < 0) {
            elv_err("Cannot create audio buffer source %d", i);
            goto end;
        }

        if ((ret = avfilter_link(abuffersrc_ctx[i], 0, join_ctx, i)) < 0) {
            elv_err("Failed to link audio src %d to join filter, ret=%d", i, ret);
            return ret;
        }

    }

    ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out", NULL, NULL, decoder_context->audio_filter_graph);
    if (ret < 0) {
        elv_err("Cannot create audio buffer sink");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_fmts",
        (uint8_t*)&enc_codec_ctx->sample_fmt, sizeof(enc_codec_ctx->sample_fmt),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("Cannot set output sample format");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_rates",
        (uint8_t*)&enc_codec_ctx->sample_rate, sizeof(enc_codec_ctx->sample_rate),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("Cannot set output sample rate");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "channel_layouts",
        (uint8_t*)&enc_codec_ctx->channel_layout,
        sizeof(enc_codec_ctx->channel_layout), AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("Cannot set output channel layout");
        goto end;
    }

    snprintf(format_args, sizeof(format_args),
             "sample_fmts=%s:sample_rates=%d:channel_layouts=0x%"PRIx64,
             av_get_sample_fmt_name(enc_codec_ctx->sample_fmt), enc_codec_ctx->sample_rate,
             (uint64_t)enc_codec_ctx->channel_layout);
    elv_log("Audio format_filter args=%s", format_args);

    ret = avfilter_graph_create_filter(&format_ctx, aformat, "format_out_0_0", format_args, NULL, decoder_context->audio_filter_graph);
    if (ret < 0) {
        elv_err("Cannot create audio format filter");
        goto end;
    }

    if ((ret = avfilter_link(join_ctx, 0, format_ctx, 0)) < 0) {
        elv_err("Failed to link audio join to format, ret=%d", ret);
        return ret;
    }

    if ((ret = avfilter_link(format_ctx, 0, buffersink_ctx, 0)) < 0) {
        elv_err("Failed to link audio format to sink, ret=%d", ret);
        return ret;
    }

    av_buffersink_set_frame_size(buffersink_ctx,
        encoder_context->codec_context[encoder_context->audio_stream_index[0]]->frame_size);

    if ((ret = avfilter_graph_config(decoder_context->audio_filter_graph, NULL)) < 0)
        goto end;

    /* Save FilteringContext */
    decoder_context->audio_buffersrc_ctx = abuffersrc_ctx;
    decoder_context->audio_buffersink_ctx = buffersink_ctx;

end:
    return ret;
}

