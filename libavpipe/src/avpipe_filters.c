/*
 * avpipe_filters.c
 */

#include "avpipe_xc.h"
#include "elv_log.h"

/*
 * @brief   Used to initialize video filter.
 * @return  Returns 0 if successful, otherwise eav_filter_init if there is an error.
 */
int
init_video_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params)
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
    elv_dbg("init_video_filters, video srcfilter args=%s", args);

    /* video_stream_index should be the same in both encoder and decoder context */
    pix_fmts[0] = encoder_context->codec_context[decoder_context->video_stream_index]->pix_fmt;

    ret = avfilter_graph_create_filter(&decoder_context->video_buffersrc_ctx, buffersrc, "in",
                                       args, NULL, decoder_context->video_filter_graph);
    if (ret < 0) {
        elv_err("init_video_filters cannot create buffer source err=%d\n", ret);
        goto end;
    }

    /* buffer video sink: to terminate the filter chain. */
    ret = avfilter_graph_create_filter(&decoder_context->video_buffersink_ctx, buffersink, "out",
                                       NULL, NULL, decoder_context->video_filter_graph);
    if (ret < 0) {
        elv_err("init_video_filters, cannot create buffer sink\n");
        goto end;
    }

    ret = av_opt_set_int_list(decoder_context->video_buffersink_ctx, "pix_fmts", pix_fmts,
                              AV_PIX_FMT_NONE, AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_video_filters, cannot set output pixel format\n");
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

    if (ret < 0)
        return eav_filter_init;

    return ret;
}

static void
get_avfilter_args(
    coderctx_t *decoder_context,
    int index,
    char *args,
    int len)
{
    AVCodecContext *dec_codec_ctx = decoder_context->codec_context[index];
    AVStream *s = decoder_context->format_context->streams[index];

    if (!dec_codec_ctx->channel_layout)
        dec_codec_ctx->channel_layout = av_get_default_channel_layout(dec_codec_ctx->channels);

    if (dec_codec_ctx->channel_layout == 0)
        snprintf(args, len,
            "time_base=%d/%d:sample_rate=%d:sample_fmt=%s:channels=%d",
            s->time_base.num, s->time_base.den,
            dec_codec_ctx->sample_rate,
            av_get_sample_fmt_name(dec_codec_ctx->sample_fmt),
            dec_codec_ctx->channels);
    else
        snprintf(args, len,
            "time_base=%d/%d:sample_rate=%d:sample_fmt=%s:channel_layout=0x%"PRIx64,
            s->time_base.num, s->time_base.den,
            dec_codec_ctx->sample_rate,
            av_get_sample_fmt_name(dec_codec_ctx->sample_fmt),
            dec_codec_ctx->channel_layout);
}

/*
 * @brief   Used to initialize audio filter.
 * @return  Returns 0 if successful.
 *          If number of audio streams are not correct returns eav_num_streams, 
 *          otherwise eav_filter_init if there is other errors.
 */
int
init_audio_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params)
{
    if (decoder_context->n_audio < 0) {
        return eav_num_streams;
    }

    char args[512];
    int ret = 0;
    AVFilterContext **abuffersrc_ctx = NULL;
    AVFilterContext *buffersink_ctx = NULL;
    AVFilterContext *format_ctx = NULL;
    const AVFilter *buffersrc = avfilter_get_by_name("abuffer");
    const AVFilter *buffersink = avfilter_get_by_name("abuffersink");
    const AVFilter *aformat = avfilter_get_by_name("aformat");
    AVFilterGraph *filter_graph;

    for (int i=0; i<encoder_context->n_audio_output; i++) {
        int audio_stream_index = decoder_context->audio_stream_index[i];

        AVCodecContext *dec_codec_ctx = decoder_context->codec_context[audio_stream_index];
        AVCodecContext *enc_codec_ctx = encoder_context->codec_context[audio_stream_index];

        if (!dec_codec_ctx) {
            elv_err("init_audio_filters, audio decoder was not initialized!");
            ret = AVERROR_UNKNOWN;
            goto end;
        }

        filter_graph = avfilter_graph_alloc();
        if (!buffersrc || !buffersink || !filter_graph) {
            elv_err("init_audio_filters, audio filtering source or sink element not found");
            ret = AVERROR_UNKNOWN;
            goto end;
        }

        get_avfilter_args(decoder_context, audio_stream_index, args, sizeof(args));
        elv_dbg("init_audio_filters, audio srcfilter args=%s", args);

        abuffersrc_ctx = decoder_context->audio_buffersrc_ctx;

        ret = avfilter_graph_create_filter(&abuffersrc_ctx[i], buffersrc, "in", args, NULL, filter_graph);
        if (ret < 0) {
            elv_err("init_audio_filters, cannot create audio buffer source");
            goto end;
        }

        ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out", NULL, NULL, filter_graph);
        if (ret < 0) {
            elv_err("init_audio_filters, cannot create audio buffer sink");
            goto end;
        }

        ret = av_opt_set_bin(buffersink_ctx, "sample_fmts",
            (uint8_t*)&enc_codec_ctx->sample_fmt, sizeof(enc_codec_ctx->sample_fmt),
            AV_OPT_SEARCH_CHILDREN);
        if (ret < 0) {
            elv_err("init_audio_filters, cannot set output sample format");
            goto end;
        }

        ret = av_opt_set_bin(buffersink_ctx, "sample_rates",
            (uint8_t*)&enc_codec_ctx->sample_rate, sizeof(enc_codec_ctx->sample_rate),
            AV_OPT_SEARCH_CHILDREN);
        if (ret < 0) {
            elv_err("init_audio_filters, cannot set output sample rate");
            goto end;
        }

        ret = av_opt_set_bin(buffersink_ctx, "channel_layouts",
            (uint8_t*)&enc_codec_ctx->channel_layout,
            sizeof(enc_codec_ctx->channel_layout), AV_OPT_SEARCH_CHILDREN);
        if (ret < 0) {
            elv_err("init_audio_filters, cannot set output channel layout");
            goto end;
        }

        snprintf(args, sizeof(args),
             "sample_fmts=%s:sample_rates=%d:channel_layouts=0x%"PRIx64,
             av_get_sample_fmt_name(enc_codec_ctx->sample_fmt), enc_codec_ctx->sample_rate,
             (uint64_t)enc_codec_ctx->channel_layout);
        elv_dbg("init_audio_filters, audio format_filter args=%s", args);

        ret = avfilter_graph_create_filter(&format_ctx, aformat, "format_out_0_0", args, NULL, filter_graph);
        if (ret < 0) {
            elv_err("init_audio_filters, cannot create audio format filter");
            goto end;
        }

        if ((ret = avfilter_link(abuffersrc_ctx[i], 0, format_ctx, 0)) < 0) {
            elv_err("init_audio_filters, failed to link audio src to format, ret=%d", ret);
            goto end;
        }

        if ((ret = avfilter_link(format_ctx, 0, buffersink_ctx, 0)) < 0) {
            elv_err("init_audio_filters, failed to link audio format to sink, ret=%d", ret);
            goto end;
        }

        av_buffersink_set_frame_size(buffersink_ctx,
            encoder_context->codec_context[audio_stream_index]->frame_size);
    
        if ((ret = avfilter_graph_config(filter_graph, NULL)) < 0)
            goto end;

        /* Fill FilteringContext */
        decoder_context->audio_filter_graph[i] = filter_graph;
        decoder_context->audio_buffersink_ctx[i] = buffersink_ctx;
        decoder_context->n_audio_filters++;
    }

end:
    if (ret < 0)
        return eav_filter_init;

    return ret;

}

/*
 * @brief   This filter pans multiple channels in one input stream to one output stereo stream.
 *          A sample equvalent ffmpeg command is something like this:
 *
 *          ffmpeg -i input.mov -vn  -filter_complex "[0:1]pan=stereo|c0<c1+0.707*c2|c1<c2+0.707*c1[aout]"
 *              -map [aout] -acodec aac -b:a 128k out.mp4
 * 
 *          This function creates a filter graph with the following structure:
 *
 *          asrc_abuffer --> af_pan --> asink_abuffer
 * 
 * @return  Returns 0 if successful.
 *          If number of audio streams are not correct returns eav_num_streams, 
 *          and eav_filter_init if there are other errors.
 */
int
init_audio_pan_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context)
{
    /* Only one innput stream is accepted for this filter */
    if (decoder_context->n_audio != 1) {
        elv_err("init_audio_pan_filters, invalid number of audio streams, n_audio=%d", decoder_context->n_audio);
        return eav_num_streams;
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
    const AVFilter *bufferformat = avfilter_get_by_name("aformat");
    AVFilterGraph *filter_graph;

    if (!dec_codec_ctx) {
        elv_err("init_audio_pan_filters, audio decoder was not initialized!");
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    filter_graph = avfilter_graph_alloc();
    if (!buffersrc || !buffersink || !filter_graph) {
        elv_err("init_audio_pan_filters, audio filtering source or sink element not found");
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    get_avfilter_args(decoder_context, decoder_context->audio_stream_index[0], args, sizeof(args));
    elv_dbg("init_audio_pan_filters srcfilter args=%s", args);

    /* decoder_context->n_audio is 1 */
    abuffersrc_ctx = decoder_context->audio_buffersrc_ctx;

    ret = avfilter_graph_create_filter(&abuffersrc_ctx[0], buffersrc, "in", args, NULL, filter_graph);
    if (ret < 0) {
        elv_err("init_audio_pan_filters, cannot create audio buffer source");
        goto end;
    }

    ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out", NULL, NULL, filter_graph);
    if (ret < 0) {
        elv_err("init_audio_pan_filters, cannot create audio buffer sink");
        goto end;
    }

    ret = avfilter_graph_create_filter(&format_ctx, bufferformat, "format", "sample_fmts=fltp:sample_rates=96000|88200|64000|48000|44100|32000|24000|22050|16000|12000|11025|8000|7350:", NULL, filter_graph);
    if (ret < 0) {
        elv_err("init_audio_pan_filters, cannot create audio buffer format");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_fmts",
        (uint8_t*)&enc_codec_ctx->sample_fmt, sizeof(enc_codec_ctx->sample_fmt),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_pan_filters, cannot set output sample format");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_rates",
        (uint8_t*)&enc_codec_ctx->sample_rate, sizeof(enc_codec_ctx->sample_rate),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_pan_filters, cannot set output sample rate");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "channel_layouts",
        (uint8_t*)&enc_codec_ctx->channel_layout,
        sizeof(enc_codec_ctx->channel_layout), AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_pan_filters, cannot set output channel layout");
        goto end;
    }

    if ((ret = avfilter_graph_parse_ptr(filter_graph, filters_descr,
                                        NULL, NULL, NULL)) < 0)
        goto end;

    /* Link audio source output to audio pan input */
    if ((ret = avfilter_link(abuffersrc_ctx[0], 0, filter_graph->filters[3], 0)) < 0) {
        elv_err("init_audio_pan_filters, failed to link audio src to pan, ret=%d", ret);
        goto end;
    }

    /* Link audio pan output to audio format input */
    if ((ret = avfilter_link(filter_graph->filters[3], 0, filter_graph->filters[2], 0)) < 0) {
        elv_err("init_audio_pan_filters, failed to link audio src to pan, ret=%d", ret);
        goto end;
    }

    /* Link audio format output to audio sink input */
    if ((ret = avfilter_link(filter_graph->filters[2], 0, filter_graph->filters[1], 0)) < 0) {
        elv_err("init_audio_pan_filters, failed to link audio pan to sink, ret=%d", ret);
        goto end;
    }

    av_buffersink_set_frame_size(buffersink_ctx,
        encoder_context->codec_context[0]->frame_size);

    if ((ret = avfilter_graph_config(filter_graph, NULL)) < 0)
        goto end;

    /* Fill FilteringContext */
    decoder_context->audio_filter_graph[0] = filter_graph;
    decoder_context->audio_buffersink_ctx[0] = buffersink_ctx;
    decoder_context->n_audio_filters++;

end:
    if (ret < 0)
        return eav_filter_init;

    return ret;

}


/*
 * @brief   This filter merges multiple mono audio and pans them into one output stereo (2 channel) or 5.1 (6 channels) stream.
 *          A sample equvalent ffmpeg command is something like this:
 *
 *          ffmpeg -y -i media/case_2_video_and_8_mono_audio.mp4 -vn -filter_complex
 *              "[0:3][0:4][0:5][0:6][0:7][0:8]amerge=inputs=6,pan=5.1|c0=c0|c1=c1|c2=c2| c3=c3|c4=c4|c5=c5[aout]"
 *              -map [aout] -acodec aac -b:a 128k case2-out.mp4
 * 
 *          This function creates a filter graph with the following structure:
 *
 *          asrc_abuffer1  ----+
 *                             |
 *          asrc_abuffer2  ----+---> af_amerge ---> af_apan ---> af_asink_abuffer
 *              ....           |
 *          asrc_abufferN  ----+
 * 
 * @return  Returns 0 if successful.
 *          If number of audio streams are not correct returns eav_num_streams, 
 *          and eav_filter_init if there are other errors.
 */
int
init_audio_merge_pan_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context)
{
    /* Only one innput stream is accepted for this filter */
    if (decoder_context->n_audio <= 1) {
        elv_err("init_audio_merge_pan_filters, invalid number of audio streams, n_audio=%d", decoder_context->n_audio);
        return eav_num_streams;
    }
    AVCodecContext *enc_codec_ctx = encoder_context->codec_context[encoder_context->audio_stream_index[0]];
    char args[512];
    int ret = 0;
    AVFilterContext **abuffersrc_ctx = NULL;
    AVFilterContext *buffersink_ctx = NULL;
    const AVFilter *buffersrc = avfilter_get_by_name("abuffer");
    const AVFilter *buffersink = avfilter_get_by_name("abuffersink");
    AVFilterGraph *filter_graph;
    char *source_names[] = {"in_0", "in_1", "in_2", "in_3", "in_4", "in_5", "in_6", "in_7"};

    filter_graph = avfilter_graph_alloc();
    if (!buffersrc || !buffersink || !filter_graph) {
        elv_err("init_audio_merge_pan_filters, audio filtering source or sink element not found");
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out", NULL, NULL, filter_graph);
    if (ret < 0) {
        elv_err("init_audio_merge_pan_filters, cannot create audio buffer sink");
        goto end;
    }
    ret = av_opt_set_bin(buffersink_ctx, "sample_fmts",
        (uint8_t*)&enc_codec_ctx->sample_fmt, sizeof(enc_codec_ctx->sample_fmt),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_merge_pan_filters, cannot set output sample format");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_rates",
        (uint8_t*)&enc_codec_ctx->sample_rate, sizeof(enc_codec_ctx->sample_rate),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_merge_pan_filters, cannot set output sample rate");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "channel_layouts",
        (uint8_t*)&enc_codec_ctx->channel_layout,
        sizeof(enc_codec_ctx->channel_layout), AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_merge_pan_filters, cannot set output channel layout");
        goto end;
    }

    if ((ret = avfilter_graph_parse_ptr(filter_graph, filters_descr,
                                        NULL, NULL, NULL)) < 0)
        goto end;

    /* decoder_context->n_audio > 1 */
    abuffersrc_ctx = decoder_context->audio_buffersrc_ctx;

    for (int i=0; i<decoder_context->n_audio; i++) {
        AVCodecContext *dec_codec_ctx = decoder_context->codec_context[decoder_context->audio_stream_index[i]];

        if (!dec_codec_ctx) {
            elv_err("init_audio_merge_pan_filters, audio decoder %d was not initialized!", i);
            ret = AVERROR_UNKNOWN;
            goto end;
        }

        get_avfilter_args(decoder_context, decoder_context->audio_stream_index[i], args, sizeof(args));
        elv_dbg("init_audio_merge_pan_filters, audio srcfilter args=%s", args);

        ret = avfilter_graph_create_filter(&abuffersrc_ctx[i], buffersrc, source_names[i], args, NULL, filter_graph);
        if (ret < 0) {
            elv_err("init_audio_merge_pan_filters, cannot create audio buffer source");
            goto end;
        }

        /* Link audio source output to audio merge input */
        if ((ret = avfilter_link(abuffersrc_ctx[i], 0, filter_graph->filters[1], i)) < 0) {
            elv_err("init_audio_merge_pan_filters, failed to link audio src to merge, ret=%d", ret);
            goto end;
        }
    }

    /* Link audio pan output to audio sink input */
    if ((ret = avfilter_link(filter_graph->filters[2], 0, filter_graph->filters[0], 0)) < 0) {
        elv_err("init_audio_merge_pan_filters, failed to link audio pan to sink, ret=%d", ret);
        goto end;
    }

    av_buffersink_set_frame_size(buffersink_ctx, encoder_context->codec_context[0]->frame_size);

    if ((ret = avfilter_graph_config(filter_graph, NULL)) < 0)
        goto end;

    /* Fill FilteringContext */
    decoder_context->audio_filter_graph[0] = filter_graph;
    decoder_context->audio_buffersink_ctx[0] = buffersink_ctx;
    decoder_context->n_audio_filters++;

end:
    if (ret < 0)
        return eav_filter_init;

    return ret;
}


/*
 * @brief   Used to initialize audio join filter.
 * @return  Returns 0 if successful.
 *          If number of audio streams are not correct returns eav_num_streams, 
 *          otherwise eav_filter_init if there is other errors.
 */
int
init_audio_join_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params)
{
    if (decoder_context->n_audio < 0 ||
        decoder_context->n_audio > MAX_AUDIO_MUX) {
        return eav_num_streams;
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

    decoder_context->audio_filter_graph[0] = avfilter_graph_alloc();
    if (!buffersrc || !buffersink || !join || !decoder_context->audio_filter_graph[0]) {
        elv_err("init_audio_join_filters, audio filtering source/sink/join filter not found");
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    abuffersrc_ctx = decoder_context->audio_buffersrc_ctx;

    /* Create join filter with n inputs */
    sprintf(args, "inputs=%d", decoder_context->n_audio);
    ret = avfilter_graph_create_filter(&join_ctx, join, "join", args, NULL, decoder_context->audio_filter_graph[0]);
    if (ret < 0) {
        elv_err("init_audio_join_filters, cannot create audio join");
        goto end;
    }

    /* For each audio input create an audio source filter and link it to join filter */
    for (int i=0; i<decoder_context->n_audio; i++) {
        int audio_stream_index = decoder_context->audio_stream_index[i];
        AVCodecContext *dec_codec_ctx = decoder_context->codec_context[audio_stream_index];
        char filt_name[32];

        if (!dec_codec_ctx) {
            elv_err("init_audio_join_filters, audio join decoder was not initialized!");
            ret = AVERROR_UNKNOWN;
            goto end;
        }

        get_avfilter_args(decoder_context, audio_stream_index, args, sizeof(args));

        sprintf(filt_name, "in_%d", i);
        elv_dbg("init_audio_join_filters, audio srcfilter=%s args=%s", filt_name, args);

        ret = avfilter_graph_create_filter(&abuffersrc_ctx[i], buffersrc, filt_name, args, NULL, decoder_context->audio_filter_graph[0]);
        if (ret < 0) {
            elv_err("init_audio_join_filters, cannot create audio buffer source %d", i);
            goto end;
        }

        if ((ret = avfilter_link(abuffersrc_ctx[i], 0, join_ctx, i)) < 0) {
            elv_err("init_audio_join_filters, failed to link audio src %d to join filter, ret=%d", i, ret);
            goto end;
        }

    }

    ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out", NULL, NULL, decoder_context->audio_filter_graph[0]);
    if (ret < 0) {
        elv_err("init_audio_join_filters, cannot create audio buffer sink");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_fmts",
        (uint8_t*)&enc_codec_ctx->sample_fmt, sizeof(enc_codec_ctx->sample_fmt),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_join_filters, cannot set output sample format");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "sample_rates",
        (uint8_t*)&enc_codec_ctx->sample_rate, sizeof(enc_codec_ctx->sample_rate),
        AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_join_filters, cannot set output sample rate");
        goto end;
    }

    ret = av_opt_set_bin(buffersink_ctx, "channel_layouts",
        (uint8_t*)&enc_codec_ctx->channel_layout,
        sizeof(enc_codec_ctx->channel_layout), AV_OPT_SEARCH_CHILDREN);
    if (ret < 0) {
        elv_err("init_audio_join_filters, cannot set output channel layout");
        goto end;
    }

    snprintf(format_args, sizeof(format_args),
             "sample_fmts=%s:sample_rates=%d:channel_layouts=0x%"PRIx64,
             av_get_sample_fmt_name(enc_codec_ctx->sample_fmt), enc_codec_ctx->sample_rate,
             (uint64_t)enc_codec_ctx->channel_layout);
    elv_dbg("init_audio_join_filters, audio format_filter args=%s", format_args);

    ret = avfilter_graph_create_filter(&format_ctx, aformat, "format_out_0_0", format_args, NULL, decoder_context->audio_filter_graph[0]);
    if (ret < 0) {
        elv_err("Cannot create audio format filter");
        goto end;
    }

    if ((ret = avfilter_link(join_ctx, 0, format_ctx, 0)) < 0) {
        elv_err("init_audio_join_filters, failed to link audio join to format, ret=%d", ret);
        goto end;
    }

    if ((ret = avfilter_link(format_ctx, 0, buffersink_ctx, 0)) < 0) {
        elv_err("init_audio_join_filters, failed to link audio format to sink, ret=%d", ret);
        return ret;
    }

    av_buffersink_set_frame_size(buffersink_ctx,
        encoder_context->codec_context[encoder_context->audio_stream_index[0]]->frame_size);

    if ((ret = avfilter_graph_config(decoder_context->audio_filter_graph[0], NULL)) < 0)
        goto end;

    /* Save FilteringContext */
    decoder_context->audio_buffersink_ctx[0] = buffersink_ctx;
    decoder_context->n_audio_filters++;

end:
    if (ret < 0)
        return eav_filter_init;

    return ret;
}

