/*
 * Copy MPEGTS bypass encoder.
 *
 * Special code path to copy MPEGTS source media (MPEGTS or SRT input) to an alternate output.
 *
 * This could be generalized to copy any source encoding. MPEGTS is special in that
 * all streams are muxed so there is only one output part (no separate video/audio)
 */

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "avpipe_format.h"
#include "avpipe_io.h"
#include "avpipe_copy_mpegts.h"
#include "elv_log.h"

static int
copy_mpegts_set_encoder_options(
    cp_ctx_t *cp_ctx,
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params,
    int stream_index,
    int timebase)
{
    if (timebase <= 0) {
        elv_err("Setting encoder options failed, invalid timebase=%d (check encoding params), url=%s",
            timebase, params->url);
        return eav_timebase;
    }

    int64_t seg_duration_ts = 0;
    float seg_duration = 0;

    /* Precalculate seg_duration_ts based on seg_duration if seg_duration is set */
    if (params->seg_duration) {
        seg_duration = atof(params->seg_duration);
        if (stream_index == decoder_context->video_stream_index)
            timebase = calc_timebase(params, 1, timebase);
        seg_duration_ts = seg_duration * timebase;
    }
    if (params->video_seg_duration_ts > 0)
        seg_duration_ts = params->video_seg_duration_ts;

    av_opt_set_int(encoder_context->format_context->priv_data, "segment_duration_ts", seg_duration_ts, 0);

    // For MPEGTS we want the continuity count to not reset at beginning of each segment (https://trac.ffmpeg.org/ticket/2828)
    av_opt_set_int(encoder_context->format_context->priv_data, "individual_header_trailer", 0, 0);

    int64_t stream_start_time = decoder_context->stream[stream_index]->start_time;
    cp_ctx->stream_start_pts = stream_start_time;
    if (stream_start_time != AV_NOPTS_VALUE) {
        // Initial offset needs to be in microseconds
        int64_t offset_microseconds = av_rescale_q(stream_start_time, (AVRational){1, timebase}, AV_TIME_BASE_Q);
        av_opt_set_int(encoder_context->format_context->priv_data, "initial_offset", offset_microseconds, 0);
        elv_log("Set initial segment offset to %"PRId64" microseconds, based on stream start time of %"PRId64" and timebase %d", offset_microseconds, stream_start_time, timebase);
    }

    elv_dbg("setting \"fmp4-segment\" video segment_time to %s, seg_duration_ts=%"PRId64", url=%s",
    params->seg_duration, seg_duration_ts, params->url);

    return eav_success;
}

static int
copy_mpegts_prepare_video_encoder(
    cp_ctx_t *cp_ctx,
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params,
    int stream_index)
{
    int rc = 0;
    AVStream *in_stream = decoder_context->stream[stream_index];
    AVStream *out_stream = encoder_context->stream[stream_index];
    AVCodecParameters *in_codecpar = in_stream->codecpar;

    encoder_context->codec[stream_index] = avcodec_find_encoder(in_stream->codec->codec_id);

    if (!encoder_context->codec[stream_index]) {
        elv_dbg("could not find the proper codec");
        return eav_codec_context;
    }


    rc = avcodec_parameters_copy(out_stream->codecpar, in_codecpar);
    if (rc < 0) {
        elv_err("copy_ts failed to copy codec parameters, url=%s", params->url);
        return eav_codec_param;
    }

    out_stream->time_base = in_stream->time_base;
    out_stream->avg_frame_rate = decoder_context->format_context->streams[decoder_context->video_stream_index]->avg_frame_rate;
    // The codec tag is a hint for decoding the stream
    out_stream->codecpar->codec_tag = in_stream->codecpar->codec_tag;

    rc = copy_mpegts_set_encoder_options(cp_ctx, encoder_context, decoder_context, params, stream_index,
        out_stream->time_base.den);
    if (rc < 0) {
        elv_err("Failed to set video encoder options with copy_ts, url=%s", params->url);
        return rc;
    }

    elv_log("Prepared video encoder for stream index %d", stream_index);

    return 0;
}

static int
copy_mpegts_prepare_audio_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params,
    int stream_index)
{
    int n_audio = encoder_context->n_audio_output;
    AVFormatContext *format_context = encoder_context->format_context;
    int rc;

    // This assignment helps to keep some of the code below more understandable
    // output stream index always equals input stream index for mpegts capture
    int output_stream_index = stream_index;

    if (stream_index < 0) {
        elv_dbg("No audio stream detected by decoder.");
        return eav_stream_index;
    }

    if (!decoder_context->codec_context[stream_index]) {
        elv_err("Decoder codec context is NULL! stream_index=%d, url=%s", stream_index, params->url);
        return eav_codec_context;
    }

    encoder_context->audio_stream_index[output_stream_index] = output_stream_index;

    encoder_context->codec[output_stream_index] = avcodec_find_encoder(decoder_context->codec_context[stream_index]->codec_id);
    if (!encoder_context->codec[output_stream_index]) {
        elv_err("Audio codec not found, codec_id=%s, url=%s",
            avcodec_get_name(decoder_context->codec_context[stream_index]->codec_id), params->url);
        return eav_codec_context;
    }

    encoder_context->codec_context[output_stream_index] = avcodec_alloc_context3(encoder_context->codec[output_stream_index]);

    /* By default use decoder parameters */
    encoder_context->codec_context[output_stream_index]->sample_rate = decoder_context->codec_context[stream_index]->sample_rate;

    /* Set the default time_base based on input sample_rate */
    encoder_context->codec_context[output_stream_index]->time_base = (AVRational){1, encoder_context->codec_context[output_stream_index]->sample_rate};
    encoder_context->stream[output_stream_index]->time_base = encoder_context->codec_context[output_stream_index]->time_base;

    encoder_context->codec_context[output_stream_index]->sample_fmt = decoder_context->codec_context[stream_index]->sample_fmt;

    if (params->channel_layout > 0)
        encoder_context->codec_context[output_stream_index]->channel_layout = params->channel_layout;
    else
        /* If the input stream is stereo the decoder_context->codec_context[index]->channel_layout is AV_CH_LAYOUT_STEREO */
        encoder_context->codec_context[output_stream_index]->channel_layout =
            get_channel_layout_for_encoder(decoder_context->codec_context[stream_index]->channel_layout);

    encoder_context->codec_context[output_stream_index]->channels = av_get_channel_layout_nb_channels(encoder_context->codec_context[output_stream_index]->channel_layout);

    encoder_context->codec_context[output_stream_index]->bit_rate = params->audio_bitrate;

    // mpeg2 audio decoder supports planar and non-planar 16-bit signed samples (S16 and S16P), preferring S16P
    // However, the mp2 audio encoder only supports non-planar.
    // The encoder will automatically swap over to the supported one _if_ there is only one channel.
    // If there are multiple channels, we need to do this conversion ourselves.
    // Sources: `libavcodec/{encode.c:ff_encode_preinit, mpegaudioenc.c, mpegaudioenc_fixed.c}`
    
    if ((encoder_context->codec_context[output_stream_index]->channels > 1)
        && (encoder_context->codec_context[output_stream_index]->sample_fmt == AV_SAMPLE_FMT_S16P)
        && ((encoder_context->codec[output_stream_index]->id == AV_CODEC_ID_MP2) || (encoder_context->codec[output_stream_index]->id == AV_CODEC_ID_MP3))) {
        elv_dbg("Converting MP2/MP3 audio encoder to non-planar format, stream_index=%d", stream_index);
        encoder_context->codec_context[output_stream_index]->sample_fmt = AV_SAMPLE_FMT_S16;
    }

    /* Allow the use of the experimental AAC encoder. */
    encoder_context->codec_context[output_stream_index]->strict_std_compliance = FF_COMPLIANCE_EXPERIMENTAL;

    /* Open audio encoder codec */
    if (avcodec_open2(encoder_context->codec_context[output_stream_index], encoder_context->codec[output_stream_index], NULL) < 0) {
        char *codec_str;
        char *ctx_str;
        av_opt_serialize(encoder_context->codec_context[output_stream_index], 0, 0, &codec_str, '=', ':');
        av_opt_serialize(encoder_context->codec_context[output_stream_index], 0, 0, &ctx_str, '=', ':');

        elv_dbg("Could not open encoder for audio, stream_index=%d, codec=%s", stream_index, encoder_context->codec[output_stream_index]->long_name);
        elv_dbg("codec=%s,\n\n ctx=%s\n", codec_str, ctx_str);
        if (codec_str)
            free(codec_str);
        if (ctx_str)
            free(ctx_str);
        return eav_open_codec;
    }

    if (avcodec_parameters_from_context(
        encoder_context->stream[output_stream_index]->codecpar,
        encoder_context->codec_context[output_stream_index]) < 0) {
        elv_err("Failed to copy encoder parameters to output stream, url=%s", params->url);
        return eav_codec_param;
    }

    return 0;
}

/*
 * Prepare the MPEGTS copy (bypass) encoder.
 * This is largely similar to the bypass section of the main 'prepare_encoder()' and must
 * be run after 'prepare_decoder()' (there is nothing special needed
 * in 'prepare_decoder()' for the MPEGTS copy operation - just the regular code path).
 */
int
copy_mpegts_prepare_encoder(
    cp_ctx_t *cp_ctx,
    coderctx_t *decoder_context,
    avpipe_io_handler_t *out_handlers,
    ioctx_t *inctx,
    xcparams_t *params)
{
    out_tracker_t *out_tracker;
    char *filename = "";
    char *format = params->format;
    int rc = 0;

    coderctx_t *encoder_context = &cp_ctx->encoder_ctx;

    encoder_context->is_mpegts = decoder_context->is_mpegts;
    encoder_context->out_handlers = out_handlers;

    format = "segment";
    filename = "ts-segment-%05d.ts";

    // Single format context for video and audio
    avformat_alloc_output_context2(&encoder_context->format_context, NULL, format, filename);
    if (!encoder_context->format_context) {
        elv_dbg("copy mpegts - could not allocate memory for video output format");
        return eav_codec_context;
    }

    /* Custom output buffer */
    encoder_context->format_context->io_open = elv_io_open;
    encoder_context->format_context->io_close = elv_io_close;

    encoder_context->n_audio_output = num_audio_output(decoder_context, params);

    for (int i = 0; i < decoder_context->format_context->nb_streams; i++) {
        AVStream *in_stream = decoder_context->format_context->streams[i];

        elv_log("Stream %d start PTS: %"PRId64"", i, in_stream->start_time);

        AVStream *out_stream = avformat_new_stream(encoder_context->format_context, NULL);
        if (!out_stream) {
            elv_err("Failed allocating output stream, url=%s", params->url);
            return eav_mem_alloc;
        }
        encoder_context->stream[i] = out_stream;

        // Copy metadata into output stream. Inspired by fftools/ffmpeg_opt.c:2671 in open_output_file
        av_dict_copy(&out_stream->metadata, in_stream->metadata, AV_DICT_DONT_OVERWRITE);


        // TODO - Evaluate if the commented out code below helps in some circumstances.

        // if (avformat_transfer_internal_stream_timing_info(encoder_context->format_context->oformat, out_stream, in_stream, -1) < 0) {
        //     elv_err("Failed to transfer internal stream timing info, url=%s", params->url);
        //     return eav_codec_context;
        // }

        // out_stream->disposition = in_stream->disposition;

        if (in_stream->nb_side_data) {
            for (int i = 0; i < in_stream->nb_side_data; i++) {
                const AVPacketSideData *sd_src = &in_stream->side_data[i];
                uint8_t *out_data;

                out_data = av_stream_new_side_data(out_stream, sd_src->type, sd_src->size);
                if (!out_data) {
                    elv_err("Failed to allocate side data, url=%s", params->url);
                    return eav_mem_alloc;
                }
                memcpy(out_data, sd_src->data, sd_src->size);
            }
        }

        // For audio/video, do more specific things. For subtitles/data/etc, just copy the stream
        switch (in_stream->codecpar->codec_type) {
            case AVMEDIA_TYPE_AUDIO:
                rc = copy_mpegts_prepare_audio_encoder(encoder_context, decoder_context, params, i);
                break;
            case AVMEDIA_TYPE_VIDEO:
                rc = copy_mpegts_prepare_video_encoder(cp_ctx, encoder_context, decoder_context, params, i);
                break;
            default:
                elv_log("Copying stream %d of type %s", i, av_get_media_type_string(in_stream->codecpar->codec_type));
                avcodec_parameters_copy(out_stream->codecpar, in_stream->codecpar);
                continue;
        }
    }

    /*
     * Allocate a single out_tracker (and out_handler) for all video and a audio streams.
     */
    out_tracker = (out_tracker_t *) calloc(1, sizeof(out_tracker_t));
    out_tracker->out_handlers = out_handlers;
    out_tracker->inctx = inctx;
    out_tracker->video_stream_index = decoder_context->video_stream_index;
    out_tracker->audio_stream_index = decoder_context->audio_stream_index[0]; // PENDING(SS) do we need this?
    out_tracker->seg_index = atoi(params->start_segment_str);
    out_tracker->encoder_ctx = encoder_context;
    out_tracker->xc_type = xc_all;
    encoder_context->format_context->avpipe_opaque = out_tracker;

    return 0;
}

static int
copy_mpegts(
    cp_ctx_t *cp_ctx,
    AVPacket *packet,
    xcparams_t *p)
{
    AVFormatContext *format_context;
    coderctx_t *encoder_context = &cp_ctx->encoder_ctx;

    format_context = encoder_context->format_context;

    if (packet->pts == AV_NOPTS_VALUE ||
        packet->dts == AV_NOPTS_VALUE ||
        packet->data == NULL) {
        elv_warn("INVALID %s PACKET (COPY) pts=%"PRId64" dts=%"PRId64" duration=%"PRId64" pos=%"PRId64" size=%d stream_index=%d flags=%x data=%p\n",
            "AUDIO/VIDEO",
            packet->pts, packet->dts, packet->duration,
            packet->pos, packet->size, packet->stream_index,
            packet->flags, packet->data);

        return eav_success; // Respect the logic in regular bypass encoder
    }

    packet->pts -= cp_ctx->stream_start_pts;
    packet->dts -= cp_ctx->stream_start_pts;

    int rc = av_interleaved_write_frame(format_context, packet);
    if (rc < 0) {
        elv_err("Failure in copying packet xc_type=%d rc=%d url=%s", p->xc_type, rc, p->url);
        return eav_write_frame;
    }

    // PENDING(SS) call avpipe_stater(outctx)

    return eav_success;
}

void *
copy_mpegts_func(
    void *p)
{
    xctx_t *xctx = (xctx_t *) p;
    cp_ctx_t *cp_ctx = &xctx->cp_ctx;

    coderctx_t *decoder_context = &xctx->decoder_ctx;
    coderctx_t *encoder_context = &cp_ctx->encoder_ctx;
    xcparams_t *params = xctx->params;
    xc_frame_t *xc_frame;
    int err = 0;

    while (!xctx->stop || elv_channel_size(cp_ctx->ch) > 0) {

        // Retrieve MPEGTS packets from the dedicated "copy mpegts" channel
        // Note xc_frame only contains a packet in this case (no frame)
        xc_frame = elv_channel_receive(cp_ctx->ch);
        if (!xc_frame) {
            elv_dbg("copy_mpegts_func, there is no frame, url=%s", params->url);
            continue;
        }
        AVPacket *packet = xc_frame->packet;
        free(xc_frame);

        if (!packet) {
            elv_err("copy_mpegts_func, packet is NULL, url=%s", params->url);
            free(xc_frame);
            continue;
        }

        err = copy_mpegts(
            cp_ctx,
            packet,
            params
        );

        av_packet_unref(packet);
        av_packet_free(&packet);

        if (err != eav_success) {
            elv_err("Stop video transcoding, err=%d, url=%s", err, params->url);
            break;
        }
    }

    if (!xctx->err)
        xctx->err = err;

    elv_channel_close(xctx->cp_ctx.ch, 0);
    elv_dbg("copy_mpegts_func err=%d, stop=%d, url=%s", err, xctx->stop, params->url);

    return NULL;
}
