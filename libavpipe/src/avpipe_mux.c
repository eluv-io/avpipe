#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"
#include <stdbool.h>

typedef struct pts_estimator_t {
    int64_t pts_per_frame[MAX_STREAMS];
    int64_t frames_written[MAX_STREAMS];
    /* PTS of the last packet read from the input stream, used to detect discrepancies. */
    int64_t last_pts[MAX_STREAMS];

    int major_discrepancies_logged;
    int minor_discrepancies_logged;
} pts_estimator_t;

/* Allocate and initialize a PTS estimator. */
static int init_pts_estimator(pts_estimator_t **estimator);
/* Set the PTS per stream based on ffmpeg's stream info. This must be called _before_ any packets have their PTS adjusted. */
static int set_pts_per_frame_from_streaminfo(pts_estimator_t *estimator, int stream_index, int64_t pts_per_frame);
static bool is_pts_per_frame_known(pts_estimator_t *estimator, int stream_index);
/* Report two successive packets for PTS estimation. This should be called at the end of retrieving
 * the second packet of the stream in order to estimate the PTS delta. */
static int report_successive_packets_for_pts_estimation(pts_estimator_t *estimator, int stream_index, AVPacket *pkt1, AVPacket *pkt2);
/* Adjust the PTS of a packet just before writing it out to the output. If anything has not been
 * initialized properly, this function will error. */
static int adjust_pts(pts_estimator_t *estimator, int stream_index, AVPacket *pkt);

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
    AVPacket *pkt,
    pts_estimator_t *pts_estimator)
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
    } else {
        if (ret != AVERROR_EOF)
            elv_err("Failed to read frame index=%d, ret=%d", index, ret);
        return ret;
    }

    if (!is_pts_per_frame_known(pts_estimator, index)) {
        ret = report_successive_packets_for_pts_estimation(pts_estimator, index, pkt, &pkts[index]);
        if (ret < 0) {
            elv_err("Failed to report successive packets for pts estimation, ret=%d", ret);
            return ret;
        }
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
    pts_estimator_t *pts_estimator;
    int *valid_pkts;
    io_mux_ctx_t *in_mux_ctx;
    int found_keyframe = 0;

    ret = init_pts_estimator(&pts_estimator);
    if (ret < 0) {
        elv_err("Failed to initialize pts estimator, ret=%d", ret);
        return ret;
    }

    if (!xctx) {
        elv_err("Invalid transcoding context for muxing");
        return eav_param;
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
        set_pts_per_frame_from_streaminfo(pts_estimator, i, av_rescale_q(1, avg_frame_rate, time_base));
    }

    for (int i=0; i < stream_count; i++) {
        ret = av_read_frame(xctx->in_muxer_ctx[i].format_context, &pkts[i]);
        if (ret >= 0) {
            valid_pkts[i] = 1;
        }
    }

    while (1) {
        ret = get_next_packet(xctx, &pkt, pts_estimator);
        if (ret < 0)
            break;
        stream_index = ret;

        if (!found_keyframe) {
            if (stream_index == 0 && pkt.flags & AV_PKT_FLAG_KEY) {
                found_keyframe = 1;
            } else {
                av_packet_unref(&pkt);
                continue;
            }
        }

        ret = adjust_pts(pts_estimator, stream_index, &pkt);
        if (ret < 0) {
            elv_err("Failed to adjust pts for stream %d, ret=%d", stream_index, ret);
            break;
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

static int init_pts_estimator(pts_estimator_t **estimator) {
    *estimator = (pts_estimator_t *)calloc(1, sizeof(pts_estimator_t));
    if (!*estimator) {
        elv_err("Failed to allocate memory for pts estimator");
        return eav_mem_alloc;
    }

    for (int i=0; i<MAX_STREAMS; i++) {
        (*estimator)->last_pts[i] = AV_NOPTS_VALUE;
        (*estimator)->pts_per_frame[i] = AV_NOPTS_VALUE;
    }

    return eav_success;
}

static int set_pts_per_frame_from_streaminfo(pts_estimator_t *estimator, int stream_index, int64_t pts_per_frame) {
    if (stream_index < 0 || stream_index >= MAX_STREAMS) {
        elv_err("Invalid stream index %d", stream_index);
        return eav_param;
    }

    for (int i=0; i<MAX_STREAMS; i++) {
        if (estimator->frames_written[i] != 0) {
            elv_err("Frames already written to stream %d before setting pts per frame", i);
            return eav_param;
        }
    }

    if (estimator->pts_per_frame[stream_index] != AV_NOPTS_VALUE) {
        elv_err("PTS per frame already set for stream %d", stream_index);
        return eav_param;
    }

    if (pts_per_frame <= 0) {
        elv_err("PTS per frame must be positive, got %"PRId64"", pts_per_frame);
        return eav_param;
    }

    elv_warn("Setting PTS per frame for stream %d to %"PRId64"", stream_index, pts_per_frame);

    estimator->pts_per_frame[stream_index] = pts_per_frame;
    return eav_success;
}

static bool is_pts_per_frame_known(pts_estimator_t *estimator, int stream_index) {
    if (stream_index < 0 || stream_index >= MAX_STREAMS) {
        elv_err("Invalid stream index %d", stream_index);
        return false;
    }
    return estimator->pts_per_frame[stream_index] != AV_NOPTS_VALUE;
}

/* Report two successive packets for PTS estimation. This should be called at the end of retrieving
 * the second packet of the stream in order to estimate the PTS delta. */
static int report_successive_packets_for_pts_estimation(pts_estimator_t *estimator, int stream_index, AVPacket *pkt1, AVPacket *pkt2) {
    if (stream_index < 0 || stream_index >= MAX_STREAMS) {
        elv_err("Invalid stream index %d", stream_index);
        return eav_param;
    }

    if (!pkt1 || !pkt2 || pkt1->pts == AV_NOPTS_VALUE || pkt2->pts == AV_NOPTS_VALUE) {
        elv_err("Invalid packets, either missing or NOPTS");
        return eav_param;
    }

    if (pkt2->pts <= pkt1->pts) {
        elv_err("Invalid packet order, pkt1 pts=%"PRId64", pkt2 pts=%"PRId64"", pkt1->pts, pkt2->pts);
        return eav_param;
    }

    estimator->pts_per_frame[stream_index] = pkt2->pts - pkt1->pts;

    return eav_success;
}

/* Adjust the PTS of a packet just before writing it out to the output. If anything has not been
 * initialized properly, this function will error. */
static int adjust_pts(pts_estimator_t *estimator, int stream_index, AVPacket *pkt) {
    if (stream_index < 0 || stream_index >= MAX_STREAMS) {
        elv_err("Invalid stream index %d", stream_index);
        return eav_param;
    }

    if (!pkt) {
        elv_err("Invalid missing packet");
        return eav_param;
    }

    if (estimator->pts_per_frame[stream_index] == AV_NOPTS_VALUE) {
        elv_err("PTS per frame not set for stream %d. Mux initialization was incorrect", stream_index);
        return eav_param;
    } else if (estimator->pts_per_frame[stream_index] <= 0) {
        elv_err("PTS per frame must be positive, got %"PRId64"", estimator->pts_per_frame[stream_index]);
        return eav_param;
    }

    if (estimator->last_pts[stream_index] != AV_NOPTS_VALUE) {
        if (pkt->pts <= estimator->last_pts[stream_index]) {
            elv_warn("Out of order packets, last_pts=%"PRId64", pkt pts=%"PRId64"", estimator->last_pts[stream_index], pkt->pts);
        }

        int64_t pts_delta = pkt->pts - estimator->last_pts[stream_index];
        int64_t expected_delta = estimator->pts_per_frame[stream_index];
        int64_t diff_from_expected = labs(pts_delta - estimator->pts_per_frame[stream_index]);

        if (diff_from_expected > (expected_delta / 5)) {
            if (estimator->major_discrepancies_logged < 100) {
                elv_warn("avpipe_mux stream %d pts_delta=%"PRId64", expected=%"PRId64", pts=%"PRId64", last_pts=%"PRId64"",
                    stream_index, pts_delta, expected_delta, pkt->pts, estimator->last_pts[stream_index]);
                estimator->major_discrepancies_logged++;
            } else if (estimator->major_discrepancies_logged == 100) {
                elv_warn("avpipe_mux done reporting major PTS discrepancies, too many logs");
                estimator->major_discrepancies_logged++;
            }
        } else if (diff_from_expected > 0) {
            if (estimator->minor_discrepancies_logged < 100) {
                elv_dbg("avpipe_mux stream %d pts_delta=%"PRId64", expected=%"PRId64", pts=%"PRId64", last_pts=%"PRId64"",
                    stream_index, pts_delta, expected_delta, pkt->pts, estimator->last_pts[stream_index]);
                estimator->minor_discrepancies_logged++;
            } else if (estimator->minor_discrepancies_logged == 100) {
                elv_dbg("avpipe_mux done reporting minor PTS discrepancies, too many logs");
                estimator->minor_discrepancies_logged++;
            }
        }
    }

    estimator->last_pts[stream_index] = pkt->pts;

    pkt->pts = estimator->frames_written[stream_index] * estimator->pts_per_frame[stream_index];
    pkt->dts = pkt->pts;

    estimator->frames_written[stream_index]++;

    return eav_success;
}
