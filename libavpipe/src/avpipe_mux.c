#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"

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
    outctx->type = avpipe_fmp4_segment;
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
    if (out_handlers) {
        out_handlers->avpipe_stater(outctx, out_stat_encoding_end_pts);
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
    AVFormatContext *format_ctx,
    int seekable);


static int
prepare_input_muxer(
    coderctx_t *muxer_ctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    txparams_t *params)
{
    int rc = 0;
    io_mux_ctx_t *in_mux_ctx = inctx->in_mux_ctx;
    int in_mux_index = inctx->in_mux_index;

    if (in_mux_index == 0) {
        inctx->url = in_mux_ctx->video.parts[0];
    } else if (in_mux_index <= in_mux_ctx->last_audio_index) {
        inctx->url = in_mux_ctx->audios[in_mux_index-1].parts[0];
    } else if (in_mux_index <= in_mux_ctx->last_audio_index + in_mux_ctx->last_caption_index) {
        inctx->url = in_mux_ctx->captions[in_mux_index-in_mux_ctx->last_audio_index-1].parts[0];
    } else {
        elv_err("prepare_input_muxer() invalid in_mux_index=%d", in_mux_index);
        return -1;
    }

    muxer_ctx->format_context = avformat_alloc_context();
    if (!muxer_ctx->format_context) {
        elv_err("Could not allocate memory for muxer format context");
        return -1;
    }

    /* set our custom reader */
    prepare_input(in_handlers, inctx, muxer_ctx->format_context, params->seekable);

    rc = avformat_open_input(&muxer_ctx->format_context, inctx->url, NULL, NULL);
    if (rc != 0) {
        elv_err("Could not open input muxer, err=%d", rc);
        return -1;
    }

    /* Retrieve stream information */
    if (avformat_find_stream_info(muxer_ctx->format_context,  NULL) < 0) {
        elv_err("Could not get input muxer stream info");
        return -1;
    }

    muxer_ctx->codec_parameters[0] = muxer_ctx->format_context->streams[0]->codecpar;
    /* Find codec and then initialize codec_context with the codec */
    muxer_ctx->codec[0] = avcodec_find_decoder(muxer_ctx->codec_parameters[0]->codec_id);
    muxer_ctx->codec_context[0] = avcodec_alloc_context3(muxer_ctx->codec[0]);
    if (!muxer_ctx->codec_context[0]) {
        elv_err("Failed to allocated memory for muxer AVCodecContext");
        return -1;
    }

    if (avcodec_parameters_to_context(muxer_ctx->codec_context[0], muxer_ctx->codec_parameters[0]) < 0) {
        elv_err("Failed to copy muxer codec params to codec context");
        return -1;
    }

    if (avcodec_open2(muxer_ctx->codec_context[0], muxer_ctx->codec[0], NULL) < 0) {
        elv_err("Failed to open codec muxer through avcodec_open2, err=%d, codec_id=%s",
                rc, avcodec_get_name(muxer_ctx->codec_parameters[0]->codec_id));
        return -1;
    }

    return 0;
}

/**
 * @brief   Initializes an io_mux_ctx_t
 *
 * @param   mux_spec        A pointer to memory containing muxing spec.
 * @param   in_mux_ctx      A pointer to io_mux_ctx_t that will be initialized based on mux_spec.
 *
 * @return  Returns 0 if transcoding is successful, otherwise -1.
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
        return -1;

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
            return -1;
        if (!strcmp(stream_type, "video") && stream_index > 1) {
            elv_err("init_mux_ctx invalid video stream_index=%d", stream_index);
            return -1;
        }
        if (!strcmp(stream_type, "audio") && (stream_index > MAX_AUDIO_MUX || stream_index > in_mux_ctx->last_audio_index+1)) {
            elv_err("init_mux_ctx invalid audio stream_index=%d", stream_index);
            return -1;
        }
        if (!strcmp(stream_type, "caption") && (stream_index > MAX_CAPTION_MUX || stream_index > in_mux_ctx->last_caption_index+1)) {
            elv_err("init_mux_ctx invalid caption stream_index=%d", stream_index);
            return -1;
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
            if (stream_index > in_mux_ctx->last_audio_index)
                in_mux_ctx->last_audio_index = stream_index;
        } else if (!strcmp(stream_type, "video") && in_mux_ctx->video.n_parts < MAX_MUX_IN_STREAM) {
            in_mux_ctx->video.parts[in_mux_ctx->video.n_parts] = stream_url;
            in_mux_ctx->video.n_parts++;
        } else if (!strcmp(stream_type, "caption") && in_mux_ctx->captions[stream_index-1].n_parts < MAX_MUX_IN_STREAM) {
            in_mux_ctx->captions[stream_index-1].parts[in_mux_ctx->captions[stream_index-1].n_parts] = stream_url;
            in_mux_ctx->captions[stream_index-1].n_parts++;
            if (stream_index > in_mux_ctx->last_caption_index)
                in_mux_ctx->last_caption_index = stream_index;
        }
    }

    if (found_muxing_input <= 0)
        return -1;

    in_mux_ctx->out_filename = strdup(out_filename);

    elv_dbg("init_mux_ctx video_stream=%d, audio_streams=%d, captions=%d",
        in_mux_ctx->video.n_parts > 0 ? 1 : 0, in_mux_ctx->last_audio_index, in_mux_ctx->last_caption_index);

    return 0;
}


/*
 * url is the output filename.
 */
int
avpipe_init_muxer(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    io_mux_ctx_t *in_mux_ctx,
    avpipe_io_handler_t *out_handlers,
    txparams_t *p,
    char *url)
{
    int ret;
    char *out_filename = url;
    txctx_t *p_txctx;

    if (init_mux_ctx(p->mux_spec, out_filename, in_mux_ctx) < 0) {
        elv_err("Initializing mux context failed");
        return -1;
    }

    p_txctx = (txctx_t *) calloc(1, sizeof(txctx_t));
    if (!txctx) {
        elv_err("Trancoding context is NULL (muxer)");
        return -1;
    }

    coderctx_t *out_muxer_ctx = &p_txctx->out_muxer_ctx;

    /* Prepare video, audio, captions input muxer */
    for (int i=0; i<in_mux_ctx->last_audio_index+in_mux_ctx->last_caption_index+1; i++) {
        ioctx_t *inctx = (ioctx_t *)calloc(1, sizeof(ioctx_t));
        inctx->in_mux_index = i;
        inctx->in_mux_ctx = in_mux_ctx;
        prepare_input_muxer(&p_txctx->in_muxer_ctx[i], in_handlers, inctx, p);
        p_txctx->inctx_muxer[i] = inctx;
    }

    /* allocate the output format context */
    //avformat_alloc_output_context2(&out_muxer_ctx->format_context, NULL, "segment", out_filename);
    avformat_alloc_output_context2(&out_muxer_ctx->format_context, NULL, "mp4", out_filename);
    if (!out_muxer_ctx->format_context) {
        elv_dbg("could not allocate memory for muxer output format");
        return -1;
    }

    /* The output format has to be fragmented to avoid doing seeks */
    av_opt_set(out_muxer_ctx->format_context->priv_data, "movflags", "frag_every_frame", 0);
    //av_opt_set(out_muxer_ctx->format_context->priv_data, "segment_format_options", "movflags=faststart", 0);

    out_muxer_ctx->format_context->avpipe_opaque = out_handlers;

    /* Custom output buffer */
    out_muxer_ctx->format_context->io_open = elv_mux_open;
    out_muxer_ctx->format_context->io_close = elv_mux_close;

    for (int i=0; i<in_mux_ctx->last_audio_index+in_mux_ctx->last_caption_index+1; i++) {
        /* Add a new stream to output format for each input in muxer context (source) */
        out_muxer_ctx->stream[i] = avformat_new_stream(out_muxer_ctx->format_context, NULL);

        if (!p_txctx->in_muxer_ctx[i].format_context) {
            elv_err("Muxer input format context is NULL, i=%d", i);
            return -1;
        }

        /* Copy input stream params to output stream params */
        AVStream *in_stream = p_txctx->in_muxer_ctx[i].format_context->streams[0];
        ret = avcodec_parameters_copy(out_muxer_ctx->stream[i]->codecpar, in_stream->codecpar);
        if (ret < 0) {
            elv_err("Failed to copy codec parameters of input stream muxer, i=%d", i);
            return -1;
        }
        out_muxer_ctx->stream[i]->time_base = in_stream->time_base;
        out_muxer_ctx->stream[i]->avg_frame_rate = in_stream->avg_frame_rate;
        out_muxer_ctx->stream[i]->r_frame_rate = in_stream->r_frame_rate;

#if 0
        /* Find codec and then initialize codec_context with the codec */
        AVCodec *codec = avcodec_find_encoder(p_txctx->in_muxer_ctx[i].codec_parameters[0]->codec_id);
        if (!codec) {
            elv_err("Could not find muxer encoder codec_id=%d", p_txctx->in_muxer_ctx[i].codec_parameters[0]->codec_id);
        }

        AVCodecContext *c = avcodec_alloc_context3(codec);
        c->time_base = out_muxer_ctx->stream[i]->time_base = in_stream->time_base;
        c->pix_fmt = p_txctx->in_muxer_ctx[i].codec_context[0]->pix_fmt;
        c->width = p_txctx->in_muxer_ctx[i].codec_context[0]->width;
        c->height = p_txctx->in_muxer_ctx[i].codec_context[0]->height;
        if (p_txctx->in_muxer_ctx[i].codec[0]->sample_fmts)
            c->sample_fmt = p_txctx->in_muxer_ctx[i].codec[0]->sample_fmts[0];
        else
            c->sample_fmt = AV_SAMPLE_FMT_FLTP;
        c->sample_rate = p_txctx->in_muxer_ctx[i].codec_context[0]->sample_rate;
        c->channels = p_txctx->in_muxer_ctx[i].codec_context[0]->channels;
        c->channel_layout = AV_CH_LAYOUT_STEREO;

        /* Initialize AVCodecContext using codec */
        if ((ret = avcodec_open2(c, codec, NULL)) < 0) {
            elv_dbg("Could not open encoder for stream %d, err=%d", i, ret);
            return -1;
        }

        /* copy AVCodecContext c values to the stream codec parameters */
        ret = avcodec_parameters_from_context(out_muxer_ctx->stream[i]->codecpar, c);
        if (ret < 0) {
            elv_err("Could not copy mux out stream parameters, ret=%d", ret);
            return -1;
        }
#endif

    }

    av_dump_format(out_muxer_ctx->format_context, 0, out_filename, 1);

    /*
     * No need to call avio_open() or avio_close() since using customized call back functions for IO.
     * avio_open(&out_muxer_ctx->format_context->pb, out_filename, AVIO_FLAG_WRITE);
     */

    ret = avformat_write_header(out_muxer_ctx->format_context, NULL);
    if (ret < 0) {
        elv_err("Error occurred when opening muxer output file '%s'", out_filename);
        return -1;
    }

    txparams_t *params = (txparams_t *) calloc(1, sizeof(txparams_t));
    *params = *p;
    p_txctx->in_mux_ctx = in_mux_ctx;
    p_txctx->params = params;
    *txctx = p_txctx;

    return 0;
}

static int
get_next_packet(
    txctx_t *txctx,
    AVPacket *pkt)
{
    io_mux_ctx_t *in_mux_ctx = txctx->in_mux_ctx;
    AVPacket *pkts = txctx->pkt_array;
    int index = 0;
    int ret = 0;
    int i;

    for (i=0; i<in_mux_ctx->last_audio_index + in_mux_ctx->last_caption_index + 1; i++) {
        if (txctx->is_pkt_valid[i]) {
            index = i;
            break;
        }
    }

    for (i=index+1; i<in_mux_ctx->last_audio_index + in_mux_ctx->last_caption_index + 1; i++) {
        if (!txctx->is_pkt_valid[i])
            continue;
        AVStream *stream1 = txctx->in_muxer_ctx[i].format_context->streams[0];
        AVStream *stream2 = txctx->in_muxer_ctx[index].format_context->streams[0];
        if (av_compare_ts(pkts[i].pts, stream1->time_base, pkts[index].pts, stream2->time_base) <= 0)
            index = i;
    }

    /* If there is no valid packet anymore return */
    if (!txctx->is_pkt_valid[index])
        return 0;

    *pkt = pkts[index];
    pkt->stream_index = index;
    txctx->is_pkt_valid[index] = 0;

    if (pkt->stream_index == 0)
        dump_packet("MUX IN ", pkt, txctx->debug_frame_level, tx_video);
    else
        dump_packet("MUX IN ", pkt, txctx->debug_frame_level, tx_audio);

read_frame_again:
    ret = av_read_frame(txctx->in_muxer_ctx[index].format_context, &pkts[index]);
    /* ret is -AVERROR_INVALIDDATA when switching to new mez file */
    if (ret >= 0) {
        if (pkts[index].pts == pkt->pts)
            goto read_frame_again;
        txctx->is_pkt_valid[index] = 1;
        if (pkt->pts > 0) {
            if (index == 0) {
                if (pkt->pts > in_mux_ctx->last_video_pts)
                    in_mux_ctx->last_video_pts = pkt->pts;
                else {
                    pkt->pts += in_mux_ctx->last_video_pts;
                    pkt->dts += in_mux_ctx->last_video_pts;
                }
            } else if (index <= in_mux_ctx->last_audio_index) {
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
    }

    return 1;
}

int
avpipe_mux(
    txctx_t *txctx)
{
    int ret = 0;
    AVPacket pkt;
    AVPacket *pkts;
    int *valid_pkts;
    io_mux_ctx_t *in_mux_ctx;

    if (!txctx) {
        elv_err("Invalid transcoding context for muxing");
        return -1;
    }

    pkts = txctx->pkt_array;
    valid_pkts = txctx->is_pkt_valid;
    in_mux_ctx = txctx->in_mux_ctx;

    for (int i=0; i<in_mux_ctx->last_caption_index + in_mux_ctx->last_audio_index + 1; i++) {
        ret = av_read_frame(txctx->in_muxer_ctx[i].format_context, &pkts[i]);
        if (ret >= 0)
            valid_pkts[i] = 1;
    }

    while (1) {
        ret = get_next_packet(txctx, &pkt);
        if (ret <= 0)
            break;

        if (pkt.stream_index == 0)
            dump_packet("MUX OUT ", &pkt, 1, tx_video);
        else
            dump_packet("MUX OUT ", &pkt, 1, tx_audio);

        if (av_interleaved_write_frame(txctx->out_muxer_ctx.format_context, &pkt) < 0) {
            elv_err("Failure in copying mux packet");
            ret = -1;
            break;
        }
        av_packet_unref(&pkt);
    }

    av_write_trailer(txctx->out_muxer_ctx.format_context);

    return ret;
}

int
avpipe_mux_fini(
    txctx_t **txctx)
{
    txctx_t *p_txctx;
    io_mux_ctx_t *in_mux_ctx;

    if (!txctx || !*txctx)
        return 0;

    p_txctx = *txctx;
    in_mux_ctx = p_txctx->in_mux_ctx;

    for (int i=0; i<in_mux_ctx->last_audio_index+in_mux_ctx->last_caption_index+1; i++) {
        avcodec_close(p_txctx->in_muxer_ctx[i].codec_context[0]);
        avcodec_free_context(&p_txctx->in_muxer_ctx[i].codec_context[0]);

        AVIOContext *avioctx = (AVIOContext *) p_txctx->in_muxer_ctx[i].format_context->pb;
        if (avioctx) {
            av_freep(&avioctx->buffer);
            av_freep(&avioctx);
        }

        avformat_close_input(&p_txctx->in_muxer_ctx[i].format_context);
        free(p_txctx->inctx_muxer[i]);
    }

    avformat_free_context(p_txctx->out_muxer_ctx.format_context);

    return avpipe_fini(txctx);
}

