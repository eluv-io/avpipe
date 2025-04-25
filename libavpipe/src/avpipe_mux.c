#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"

void
log_params(
    xcparams_t *params);

int
elv_mux_open(
    struct AVFormatContext *format_ctx,
    AVIOContext **pb,
    const char *url,
    int flags,
    AVDictionary **options)
{
    int ret = 0;
    avpipe_io_handler_t *out_handlers = (avpipe_io_handler_t *) format_ctx->avpipe_opaque;
    ioctx_t *outctx = (ioctx_t *) calloc(1, sizeof(ioctx_t));

    /* Set mux output format to avpipe_fmp4_segment */
    outctx->type = avpipe_mp4_segment;
    outctx->url = (char *) url;
    elv_dbg("OUT elv_mux_open url=%s", url);
    if (out_handlers->avpipe_opener(url, outctx) < 0) {
        free(outctx);
        return -1;
    }

    AVIOContext *avioctx = avio_alloc_context(outctx->buf, outctx->bufsz, AVIO_FLAG_WRITE, (void *)outctx,
            out_handlers->avpipe_reader, out_handlers->avpipe_writer, out_handlers->avpipe_seeker);

    avioctx->direct = 0;
    (*pb) = avioctx;

    return ret;
}

void
elv_mux_close(
    struct AVFormatContext *format_ctx,
    AVIOContext *pb)
{
    avpipe_io_handler_t *out_handlers = (avpipe_io_handler_t *) format_ctx->avpipe_opaque;
    ioctx_t *outctx = (ioctx_t *)pb->opaque;

    elv_dbg("OUT elv_mux_close avioctx=%p", pb);
    if (out_handlers && outctx) {
        if (outctx->type == avpipe_video_fmp4_segment)
            out_handlers->avpipe_stater(outctx, 0, out_stat_encoding_end_pts);
        else
            out_handlers->avpipe_stater(outctx, 1, out_stat_encoding_end_pts);
        out_handlers->avpipe_closer(outctx);
    }
    free(outctx);
    free(pb);
    return;

}

extern int
prepare_input(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    coderctx_t *decoder_context,
    int seekable);


static int
prepare_input_muxer(
    coderctx_t *muxer_ctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    xcparams_t *params)
{
    int rc = 0;
    io_mux_ctx_t *in_mux_ctx = inctx->in_mux_ctx;
    int in_mux_index = inctx->in_mux_index;

    if (in_mux_index == 0) {
        inctx->url = in_mux_ctx->video.parts[0];
    } else if (in_mux_index <= in_mux_ctx->audio_count) {
        inctx->url = in_mux_ctx->audios[in_mux_index-1].parts[0];
    } else if (in_mux_index <= in_mux_ctx->audio_count + in_mux_ctx->caption_count) {
        inctx->url = in_mux_ctx->captions[in_mux_index-in_mux_ctx->audio_count-1].parts[0];
    } else {
        elv_err("prepare_input_muxer() invalid in_mux_index=%d", in_mux_index);
        return eav_stream_index;
    }

    muxer_ctx->format_context = avformat_alloc_context();
    if (!muxer_ctx->format_context) {
        elv_err("Could not allocate memory for muxer format context");
        return eav_mem_alloc;
    }

    /* set our custom reader */
    prepare_input(in_handlers, inctx, muxer_ctx, params->seekable);

    rc = avformat_open_input(&muxer_ctx->format_context, inctx->url, NULL, NULL);
    if (rc != 0) {
        elv_err("Could not open input muxer, err=%d, url=%s", rc, inctx->url);
        return eav_open_input;
    }

    /* Retrieve stream information */
    if (avformat_find_stream_info(muxer_ctx->format_context,  NULL) < 0) {
        elv_err("Could not get input muxer stream info");
        return eav_stream_info;
    }

    muxer_ctx->codec_parameters[0] = muxer_ctx->format_context->streams[0]->codecpar;
    /* Find codec and then initialize codec_context with the codec */
    muxer_ctx->codec[0] = avcodec_find_decoder(muxer_ctx->codec_parameters[0]->codec_id);
    muxer_ctx->codec_context[0] = avcodec_alloc_context3(muxer_ctx->codec[0]);
    if (!muxer_ctx->codec_context[0]) {
        elv_err("Failed to allocated memory for muxer AVCodecContext");
        return eav_mem_alloc;
    }

    if (avcodec_parameters_to_context(muxer_ctx->codec_context[0], muxer_ctx->codec_parameters[0]) < 0) {
        elv_err("Failed to copy muxer codec params to codec context");
        return eav_codec_param;
    }

    if (avcodec_open2(muxer_ctx->codec_context[0], muxer_ctx->codec[0], NULL) < 0) {
        elv_err("Failed to open codec muxer through avcodec_open2, err=%d, codec_id=%s",
                rc, avcodec_get_name(muxer_ctx->codec_parameters[0]->codec_id));
        return eav_open_codec;
    }

    return eav_success;
}

/**
 * @brief   Initializes an io_mux_ctx_t
 *
 * @param   mux_spec        A pointer to memory containing muxing spec.
 * @param   in_mux_ctx      A pointer to io_mux_ctx_t that will be initialized based on mux_spec.
 *
 * @return  Returns 0 if mux ctx initialization is successful, otherwise -1.
 */
static int
init_mux_ctx(
    char *mux_spec,
    char *out_filename,
    io_mux_ctx_t *in_mux_ctx)
{
    char *ptr;
    int found_muxing_input = 0;

    char *mux_type = strtok_r(mux_spec, "\n\r", &ptr);
    if (!mux_type || (strcmp(mux_type, "mez-mux") && strcmp(mux_type, "abr-mux")))
        return eav_param;

    in_mux_ctx->mux_type = mux_type;

    while (1) {
        char *stream_type = strtok_r(NULL, "\n\r,", &ptr);
        if (!stream_type)
            break;
        char *index_str = strtok_r(NULL, "\n\r,", &ptr);
        if (!index_str)
            break;
        char *end;
        int stream_index = (int) strtol(index_str, &end, 10);
        if (*end != '\0' || stream_index <= 0)
            return eav_param;
        if (!strcmp(stream_type, "video") && stream_index > 1) {
            elv_err("init_mux_ctx invalid video stream_index=%d", stream_index);
            return eav_param;
        }
        if (!strcmp(stream_type, "audio") && (stream_index > MAX_STREAMS || stream_index > in_mux_ctx->audio_count+1)) {
            elv_err("init_mux_ctx invalid audio stream_index=%d", stream_index);
            return eav_param;
        }
        if (!strcmp(stream_type, "caption") && (stream_index > MAX_STREAMS || stream_index > in_mux_ctx->caption_count+1)) {
            elv_err("init_mux_ctx invalid caption stream_index=%d", stream_index);
            return eav_param;
        }
        char *stream_url = strtok_r(NULL, "\n\r,", &ptr);
        if (!stream_url)
            break;

        if (strcmp(stream_type, "audio") &&
            strcmp(stream_type, "video") &&
            strcmp(stream_type, "caption"))
            continue;

        found_muxing_input++;

        if (!strcmp(stream_type, "audio") && in_mux_ctx->audios[stream_index-1].n_parts < MAX_MUX_IN_STREAM) {
            in_mux_ctx->audios[stream_index-1].parts[in_mux_ctx->audios[stream_index-1].n_parts] = stream_url;
            in_mux_ctx->audios[stream_index-1].n_parts++;
            if (stream_index > in_mux_ctx->audio_count)
                in_mux_ctx->audio_count = stream_index;
        } else if (!strcmp(stream_type, "video") && in_mux_ctx->video.n_parts < MAX_MUX_IN_STREAM) {
            in_mux_ctx->video.parts[in_mux_ctx->video.n_parts] = stream_url;
            in_mux_ctx->video.n_parts++;
        } else if (!strcmp(stream_type, "caption") && in_mux_ctx->captions[stream_index-1].n_parts < MAX_MUX_IN_STREAM) {
            in_mux_ctx->captions[stream_index-1].parts[in_mux_ctx->captions[stream_index-1].n_parts] = stream_url;
            in_mux_ctx->captions[stream_index-1].n_parts++;
            if (stream_index > in_mux_ctx->caption_count)
                in_mux_ctx->caption_count = stream_index;
        }
    }

    if (found_muxing_input <= 0)
        return eav_param;

    in_mux_ctx->out_filename = strdup(out_filename);

    elv_dbg("init_mux_ctx video_stream=%d, audio_streams=%d, captions=%d",
        in_mux_ctx->video.n_parts > 0 ? 1 : 0, in_mux_ctx->audio_count, in_mux_ctx->caption_count);

    return eav_success;
}


/*
 * url is the output filename.
 */
int
avpipe_init_muxer(
    xctx_t **xctx,
    avpipe_io_handler_t *in_handlers,
    io_mux_ctx_t *in_mux_ctx,
    avpipe_io_handler_t *out_handlers,
    xcparams_t *p)
{
    int ret;
    char *out_filename = p->url;
    xctx_t *p_xctx;

    in_mux_ctx->video.parts = (char **) calloc(MAX_MUX_IN_STREAM, sizeof(char *));
    for (int stream=0; stream<MAX_STREAMS; stream++) {
        in_mux_ctx->audios[stream].parts = (char **) calloc(MAX_MUX_IN_STREAM, sizeof(char *));
        in_mux_ctx->captions[stream].parts = (char **) calloc(MAX_MUX_IN_STREAM, sizeof(char *));
    }

    log_params(p);

    if ((ret = init_mux_ctx(p->mux_spec, out_filename, in_mux_ctx)) != eav_success) {
        elv_err("Initializing mux context failed, ret=%d", ret);
        return ret;
    }

    p_xctx = (xctx_t *) calloc(1, sizeof(xctx_t));
    if (!xctx) {
        elv_err("Trancoding context is NULL (muxer)");
        return eav_mem_alloc;
    }

    coderctx_t *out_muxer_ctx = &p_xctx->out_muxer_ctx;

    /* Prepare video, audio, captions input muxer */
    for (int i=0; i<in_mux_ctx->audio_count+in_mux_ctx->caption_count+1; i++) {
        ioctx_t *inctx = (ioctx_t *)calloc(1, sizeof(ioctx_t));
        inctx->in_mux_index = i;
        inctx->in_mux_ctx = in_mux_ctx;
        inctx->params = p;
        prepare_input_muxer(&p_xctx->in_muxer_ctx[i], in_handlers, inctx, p);
        p_xctx->inctx_muxer[i] = inctx;
    }

    /* allocate the output format context */
    //avformat_alloc_output_context2(&out_muxer_ctx->format_context, NULL, "segment", out_filename);
    avformat_alloc_output_context2(&out_muxer_ctx->format_context, NULL, "mp4", out_filename);
    if (!out_muxer_ctx->format_context) {
        elv_dbg("could not allocate memory for muxer output format");
        return eav_codec_context;
    }

    /* The output format has to be fragmented to avoid doing seeks in the output.
     * Don't set frag_every_frame in movflags because it causes audio/video to become out of sync
     * when doing random access on the muxed file.
     */
    if (p->format && !strcmp(p->format, "fmp4-segment")) {
        av_opt_set(out_muxer_ctx->format_context->priv_data, "movflags", "frag_keyframe", 0);
        av_opt_set(out_muxer_ctx->format_context->priv_data, "movflags", "cmaf", 0);
    }

    out_muxer_ctx->format_context->avpipe_opaque = out_handlers;

    /* Custom output buffer */
    out_muxer_ctx->format_context->io_open = elv_mux_open;
    out_muxer_ctx->format_context->io_close = elv_mux_close;

    for (int i=0; i<in_mux_ctx->audio_count+in_mux_ctx->caption_count+1; i++) {
        /* Add a new stream to output format for each input in muxer context (source) */
        out_muxer_ctx->stream[i] = avformat_new_stream(out_muxer_ctx->format_context, NULL);

        if (!p_xctx->in_muxer_ctx[i].format_context) {
            elv_err("Muxer input format context is NULL, i=%d", i);
            return eav_codec_context;
        }

        /* Copy input stream params to output stream params */
        AVStream *in_stream = p_xctx->in_muxer_ctx[i].format_context->streams[0];
        ret = avcodec_parameters_copy(out_muxer_ctx->stream[i]->codecpar, in_stream->codecpar);
        if (ret < 0) {
            elv_err("Failed to copy codec parameters of input stream muxer, i=%d", i);
            return eav_codec_param;
        }
        out_muxer_ctx->stream[i]->time_base = in_stream->time_base;
        out_muxer_ctx->stream[i]->avg_frame_rate = in_stream->avg_frame_rate;
        out_muxer_ctx->stream[i]->r_frame_rate = in_stream->r_frame_rate;

    }

    av_dump_format(out_muxer_ctx->format_context, 0, out_filename, 1);

    /*
     * No need to call avio_open() or avio_close() since using customized call back functions for IO.
     * avio_open(&out_muxer_ctx->format_context->pb, out_filename, AVIO_FLAG_WRITE);
     */

    ret = avformat_write_header(out_muxer_ctx->format_context, NULL);
    if (ret < 0) {
        elv_err("Error occurred when opening muxer output file '%s'", out_filename);
        return eav_write_header;
    }

    xcparams_t *params = (xcparams_t *) calloc(1, sizeof(xcparams_t));
    *params = *p;
    p_xctx->in_mux_ctx = in_mux_ctx;
    p_xctx->params = params;
    p_xctx->in_handlers = in_handlers;
    p_xctx->out_handlers = out_handlers;
    p_xctx->debug_frame_level = params->debug_frame_level;
    *xctx = p_xctx;

    return eav_success;
}

/* get_next_packet retrieves the muxed packet with the smallest pts value of all streams with packets
 * remaining and fills pkt, returning the index of the stream or an error if the return value is negative. */
static int
get_next_packet(
    xctx_t *xctx,
    AVPacket *pkt)
{
    io_mux_ctx_t *in_mux_ctx = xctx->in_mux_ctx;
    AVPacket *pkts = xctx->pkt_array;
    int index = 0;
    int ret = 0;
    int i;

    for (i=0; i<in_mux_ctx->audio_count + in_mux_ctx->caption_count + 1; i++) {
        if (xctx->is_pkt_valid[i]) {
            index = i;
            break;
        }
    }

    for (i=index+1; i<in_mux_ctx->audio_count + in_mux_ctx->caption_count + 1; i++) {
        if (!xctx->is_pkt_valid[i])
            continue;
        AVStream *stream1 = xctx->in_muxer_ctx[i].format_context->streams[0];
        AVStream *stream2 = xctx->in_muxer_ctx[index].format_context->streams[0];
        if (av_compare_ts(pkts[i].pts, stream1->time_base, pkts[index].pts, stream2->time_base) <= 0) {
            index = i;
        }
    }

    /* If there is no valid packet anymore return */
    if (!xctx->is_pkt_valid[index])
        return 0;

    *pkt = pkts[index];
    pkt->stream_index = index;
    xctx->is_pkt_valid[index] = 0;

    pkt->dts = pkt->pts;
    dump_packet(pkt->stream_index, "MUX IN ", pkt, xctx->debug_frame_level);

read_frame_again:
    ret = av_read_frame(xctx->in_muxer_ctx[index].format_context, &pkts[index]);
    /* ret is -AVERROR_INVALIDDATA when switching to new mez file */
    if (ret >= 0) {
        if (pkts[index].pts == pkt->pts)
            goto read_frame_again;
        xctx->is_pkt_valid[index] = 1;
        if (pkt->pts >= 0) {
            if (index == 0) {
                if (pkt->pts > in_mux_ctx->last_video_pts)
                    in_mux_ctx->last_video_pts = pkt->pts;
                else {
                    pkt->pts += in_mux_ctx->last_video_pts;
                    pkt->dts += in_mux_ctx->last_video_pts;
                }
            } else if (index <= in_mux_ctx->audio_count) {
                if (pkt->pts > in_mux_ctx->last_audio_pts)
                    in_mux_ctx->last_audio_pts = pkt->pts;
                else {
                    pkt->pts += in_mux_ctx->last_audio_pts;
                    pkt->dts += in_mux_ctx->last_audio_pts;
                }
            }
        }
    } else {
        if (ret != AVERROR_EOF)
            elv_err("Failed to read frame index=%d, ret=%d", index, ret);
        return ret;
    }

    return index;
}

int
avpipe_mux(
    xctx_t *xctx)
{
    int ret = 0;
    int stream_index;
    AVPacket pkt;
    AVPacket *pkts;
    int *valid_pkts;
    io_mux_ctx_t *in_mux_ctx;
    // last_pts_array is used to store the last pts of each stream in order to detect surprising
    // gaps between successive frames
    int64_t last_pts_array[MAX_STREAMS];
    // pts_per_frame is used to store the pts delta between successive frames used to stamp the
    // packets of the mux output
    int64_t pts_per_frame[MAX_STREAMS];
    // next_pts_array holds the pts to be used for the next packet written in each stream
    int64_t next_pts_array[MAX_STREAMS];
    int found_keyframe = 0;


    if (!xctx) {
        elv_err("Invalid transcoding context for muxing");
        return eav_param;
    }

    for (int i=0; i<MAX_STREAMS; i++) {
        last_pts_array[i] = AV_NOPTS_VALUE;
        pts_per_frame[i] = AV_NOPTS_VALUE;
        next_pts_array[i] = 0;
    }

    pkts = xctx->pkt_array;
    valid_pkts = xctx->is_pkt_valid;
    in_mux_ctx = xctx->in_mux_ctx;
    int stream_count = in_mux_ctx->audio_count + in_mux_ctx->caption_count + 1;

    // Set pts_per_frame for streams that have frame rate info
    for (int i=0; i < stream_count; i++) {
        AVRational avg_frame_rate = xctx->in_muxer_ctx[i].format_context->streams[0]->avg_frame_rate;
        AVRational time_base = xctx->in_muxer_ctx[i].format_context->streams[0]->time_base;
        if (avg_frame_rate.num == 0 || time_base.num == 0) {
            elv_dbg("avpipe_mux stream %d avg_frame_rate or time_base is 0", i);
            continue;
        }
        pts_per_frame[i] = av_rescale_q(1, av_inv_q(time_base), avg_frame_rate);
        elv_warn("avpipe_mux stream %d avg_frame_rate %d/%d tb %d/%d, pts per frame: %d", i, avg_frame_rate.num, avg_frame_rate.den, time_base.num, time_base.den, pts_per_frame[i]);
    }

    for (int i=0; i < stream_count; i++) {

        ret = av_read_frame(xctx->in_muxer_ctx[i].format_context, &pkts[i]);
        if (ret >= 0) {
            valid_pkts[i] = 1;
            last_pts_array[i] = pkts[i].pts;
        }
    }

    while (1) {
        ret = get_next_packet(xctx, &pkt);
        if (ret < 0)
            break;
        stream_index = ret;

        if (last_pts_array[stream_index] != AV_NOPTS_VALUE && found_keyframe) {
            if (pts_per_frame[stream_index] == AV_NOPTS_VALUE) {
                // set pts per frame, because this is the second packet we have seen for this stream
                pts_per_frame[stream_index] = pkt.pts - last_pts_array[stream_index];
            } else {
                int64_t pts_delta = pkt.pts - last_pts_array[stream_index];
                // Check if the pts delta is more than 20% away from what we expected
                if (labs(pts_delta - pts_per_frame[stream_index]) > (pts_per_frame[stream_index] / 5)) {
                    elv_dbg("avpipe_mux stream %d pts_delta=%"PRId64", expected=%"PRId64", pts=%"PRId64", last_pts=%"PRId64"",
                        stream_index, pts_delta, pts_per_frame[stream_index], pkt.pts, last_pts_array[stream_index]);
                }
            }
        }
        last_pts_array[stream_index] = pkt.pts;

        if (stream_index == 0 && pkt.flags & AV_PKT_FLAG_KEY)
            found_keyframe = 1;

        if (!found_keyframe) {
            av_packet_unref(&pkt);
            continue;
        }

        if (next_pts_array[stream_index] == AV_NOPTS_VALUE) {
            // We didn't know the PTS per frame when we wrote the first packet at 0, so we set
            // next_pts_array to the pts of the second packet, which should be exactly the PTS per
            // frame because the first packet is at 0
            next_pts_array[stream_index] = pts_per_frame[stream_index];
        }

        /* Adjust PTS and DTS and start always from 0 */
        pkt.pts = next_pts_array[stream_index];
        pkt.dts = pkt.pts;

        if (pts_per_frame[stream_index] != AV_NOPTS_VALUE) {
            next_pts_array[stream_index] += pts_per_frame[stream_index];
        } else {
            // We don't yet know the pts per frame, so we set the next_pts_array to a sentinel value
            next_pts_array[stream_index] = AV_NOPTS_VALUE;
        }

        dump_packet(pkt.stream_index, "MUX OUT ", &pkt, xctx->debug_frame_level);

        if (av_interleaved_write_frame(xctx->out_muxer_ctx.format_context, &pkt) < 0) {
            elv_err("Failure in copying mux packet");
            ret = eav_write_frame;
            break;
        }
        av_packet_unref(&pkt);
    }

    av_write_trailer(xctx->out_muxer_ctx.format_context);

    if (ret == AVERROR_EOF)
        ret = 0;

    elv_log("avpipe_mux done url=%s, rc=%d, xctx->err=%d",
        xctx->params->url, ret, xctx->err);

    return ret;
}

int
avpipe_mux_fini(
    xctx_t **xctx)
{
    xctx_t *p_xctx;
    io_mux_ctx_t *in_mux_ctx;

    if (!xctx || !*xctx)
        return 0;

    p_xctx = *xctx;
    in_mux_ctx = p_xctx->in_mux_ctx;

    for (int i=0; i<in_mux_ctx->audio_count+in_mux_ctx->caption_count+1; i++) {
        avcodec_close(p_xctx->in_muxer_ctx[i].codec_context[0]);
        avcodec_free_context(&p_xctx->in_muxer_ctx[i].codec_context[0]);

        AVIOContext *avioctx = (AVIOContext *) p_xctx->in_muxer_ctx[i].format_context->pb;
        if (avioctx) {
            av_freep(&avioctx->buffer);
            av_freep(&avioctx);
        }

        avformat_close_input(&p_xctx->in_muxer_ctx[i].format_context);
        free(p_xctx->inctx_muxer[i]);
    }

    /* Free avioctx of the output */
    {
        coderctx_t *out_muxer_ctx = &p_xctx->out_muxer_ctx;
        if (out_muxer_ctx && out_muxer_ctx->format_context) {
            AVIOContext *avioctx = (AVIOContext *) out_muxer_ctx->format_context->pb;
            if (avioctx) {
                av_freep(&avioctx->buffer);
                av_freep(&avioctx);
            }
        }
    }

    avformat_free_context(p_xctx->out_muxer_ctx.format_context);

    free(in_mux_ctx->video.parts);
    for (int stream=0; stream<MAX_STREAMS; stream++) {
        free(in_mux_ctx->audios[stream].parts);
        free(in_mux_ctx->captions[stream].parts);
    }

    return avpipe_fini(xctx);
}

