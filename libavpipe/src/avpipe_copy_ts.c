/*
 * Copy TS
 *
 * Special code path to copy the source media to output parts.
 *
 * This could be generalized to copy any source encoding. MPEGTS is special in that
 * all streams are mux'd so there is only one output part (no separate video/audio)
 */

#include <libavutil/log.h>
#include "libavutil/audio_fifo.h"
#include <libswscale/swscale.h>
#include <libavutil/imgutils.h>
#include <libavutil/display.h>

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "avpipe_format.h"
#include "elv_log.h"
#include "elv_time.h"
#include "url_parser.h"
#include "avpipe_version.h"

#include <stdio.h>
#include <fcntl.h>
#include <assert.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/uio.h>
#include <unistd.h>
#include <stdlib.h>
#include <pthread.h>

// PENDING(SS) Move elv_io to avpipe_io.h
extern int
elv_io_open(
    struct AVFormatContext *s,
    AVIOContext **pb,
    const char *url,
    int flags,
    AVDictionary **options);

extern void
elv_io_close(
    struct AVFormatContext *s,
    AVIOContext *pb);

int copy_mpegts_init(xctx_t *xctx){

    return eav_success;
}

static int
copy_mpegts_prepare_video_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int rc = 0;
    int index = decoder_context->video_stream_index;

    if (index < 0) {
        elv_dbg("No video stream detected by decoder.");
        return eav_stream_index;
    }

    encoder_context->video_stream_index = index;
    encoder_context->video_last_dts = AV_NOPTS_VALUE;
    encoder_context->stream[index] = avformat_new_stream(encoder_context->format_context, NULL);
    encoder_context->codec[index] = avcodec_find_encoder_by_name(params->ecodec);

    /* Custom output buffer */
    encoder_context->format_context->io_open = elv_io_open;
    encoder_context->format_context->io_close = elv_io_close;

    if (!encoder_context->codec[index]) {
        elv_dbg("could not find the proper codec");
        return eav_codec_context;
    }
    elv_log("Found encoder index=%d, %s", index, params->ecodec);

    AVStream *in_stream = decoder_context->stream[index];
    AVStream *out_stream = encoder_context->stream[index];
    AVCodecParameters *in_codecpar = in_stream->codecpar;

    rc = avcodec_parameters_copy(out_stream->codecpar, in_codecpar);
    if (rc < 0) {
        elv_err("BYPASS failed to copy codec parameters, url=%s", params->url);
        return eav_codec_param;
    }

    encoder_context->codec_context[index] = avcodec_alloc_context3(encoder_context->codec[index]);
    if (!encoder_context->codec_context[index]) {
        elv_dbg("could not allocated memory for codec context");
        return eav_codec_context;
    }

    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];

    // This needs to be set before open (ffmpeg samples have it wrong)
    if (encoder_context->format_context->oformat->flags & AVFMT_GLOBALHEADER) {
        encoder_codec_context->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    }

    /* Open video encoder (initialize the encoder codec_context[i] using given codec[i]). */
    if ((rc = avcodec_open2(encoder_context->codec_context[index], encoder_context->codec[index], NULL)) < 0) {
        elv_dbg("Could not open encoder for video, err=%d", rc);
        return eav_open_codec;
    }

    /* Set stream parameters after avcodec_open2() */
    if (avcodec_parameters_from_context(
            encoder_context->stream[index]->codecpar,
            encoder_context->codec_context[index]) < 0) {
        elv_dbg("could not copy encoder parameters to output stream");
        return eav_codec_param;
    }

    return 0;
}

static int
copy_mpegts_prepare_audio_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int n_audio = encoder_context->n_audio_output;
    char *ecodec;
    AVFormatContext *format_context;

    if (params->xc_type == xc_audio_merge ||
        params->xc_type == xc_audio_join ||
        params->xc_type == xc_audio_pan) {
        // Only we have one output audio in these cases
        n_audio = 1;
    }

    for (int i=0; i<n_audio; i++) {
        int stream_index = decoder_context->audio_stream_index[i];
        int output_stream_index = stream_index;

        if (params->xc_type == xc_audio_merge ||
            params->xc_type == xc_audio_join ||
            params->xc_type == xc_audio_pan) {
            // Only we have one output audio in these cases
            output_stream_index = 0;
        }

        if (stream_index < 0) {
            elv_dbg("No audio stream detected by decoder.");
            return eav_stream_index;
        }

        if (!decoder_context->codec_context[stream_index]) {
            elv_err("Decoder codec context is NULL! stream_index=%d, url=%s", stream_index, params->url);
            return eav_codec_context;
        }

        /* If there are more than 1 audio stream do encode, we can't do bypass */
        if (params && params->bypass_transcoding && decoder_context->n_audio > 1) {
            elv_err("Can not bypass multiple audio streams, n_audio=%d, url=%s", decoder_context->n_audio, params->url);
            return eav_num_streams;
        }

        format_context = encoder_context->format_context2[i];
        ecodec = params->ecodec2;
        encoder_context->audio_last_dts[i] = AV_NOPTS_VALUE;

        encoder_context->audio_stream_index[output_stream_index] = output_stream_index;
        encoder_context->n_audio = 1;

        encoder_context->stream[output_stream_index] = avformat_new_stream(format_context, NULL);
        if (params->bypass_transcoding)
            encoder_context->codec[output_stream_index] = avcodec_find_encoder(decoder_context->codec_context[stream_index]->codec_id);
        else
            encoder_context->codec[output_stream_index] = avcodec_find_encoder_by_name(ecodec);
        if (!encoder_context->codec[output_stream_index]) {
            elv_err("Codec not found, codec_id=%s, url=%s",
                avcodec_get_name(decoder_context->codec_context[stream_index]->codec_id), params->url);
            return eav_codec_context;
        }

        encoder_context->codec_context[output_stream_index] = avcodec_alloc_context3(encoder_context->codec[output_stream_index]);

        /* By default use decoder parameters */
        encoder_context->codec_context[output_stream_index]->sample_rate = decoder_context->codec_context[stream_index]->sample_rate;

        /* Set the default time_base based on input sample_rate */
        encoder_context->codec_context[output_stream_index]->time_base = (AVRational){1, encoder_context->codec_context[output_stream_index]->sample_rate};
        encoder_context->stream[output_stream_index]->time_base = encoder_context->codec_context[output_stream_index]->time_base;

        if (decoder_context->codec[stream_index] &&
            decoder_context->codec[stream_index]->sample_fmts && params->bypass_transcoding)
            encoder_context->codec_context[output_stream_index]->sample_fmt = decoder_context->codec[stream_index]->sample_fmts[0];
        else if (encoder_context->codec[output_stream_index]->sample_fmts && encoder_context->codec[output_stream_index]->sample_fmts[0])
            encoder_context->codec_context[output_stream_index]->sample_fmt = encoder_context->codec[output_stream_index]->sample_fmts[0];
        else
            encoder_context->codec_context[output_stream_index]->sample_fmt = AV_SAMPLE_FMT_FLTP;

        if (params->channel_layout > 0)
            encoder_context->codec_context[output_stream_index]->channel_layout = params->channel_layout;

        encoder_context->codec_context[output_stream_index]->channels = av_get_channel_layout_nb_channels(encoder_context->codec_context[output_stream_index]->channel_layout);

        encoder_context->codec_context[output_stream_index]->bit_rate = params->audio_bitrate;

        /* Allow the use of the experimental AAC encoder. */
        encoder_context->codec_context[output_stream_index]->strict_std_compliance = FF_COMPLIANCE_EXPERIMENTAL;

        AVCodecContext *encoder_codec_context = encoder_context->codec_context[output_stream_index];

        /* Some container formats (like MP4) require global headers to be present.
         * Mark the encoder so that it behaves accordingly. */
        if (format_context->oformat->flags & AVFMT_GLOBALHEADER)
            encoder_codec_context->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

        /* Open audio encoder codec */
        if (avcodec_open2(encoder_context->codec_context[output_stream_index], encoder_context->codec[output_stream_index], NULL) < 0) {
            elv_dbg("Could not open encoder for audio, stream_index=%d", stream_index);
            return eav_open_codec;
        }

        if (avcodec_parameters_from_context(
            encoder_context->stream[output_stream_index]->codecpar,
            encoder_context->codec_context[output_stream_index]) < 0) {
            elv_err("Failed to copy encoder parameters to output stream, url=%s", params->url);
            return eav_codec_param;

        }
    }

    return 0;
}

int
copy_mpegts_prepare_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    avpipe_io_handler_t *out_handlers,
    ioctx_t *inctx,
    xcparams_t *params)
{
    out_tracker_t *out_tracker;
    char *filename = "";
    char *format = params->format;
    int rc = 0;

    encoder_context->is_mpegts = decoder_context->is_mpegts;
    encoder_context->out_handlers = out_handlers;

    format = "segment";
    filename = "ts-segment-%05d.ts";

    avformat_alloc_output_context2(&encoder_context->format_context, NULL, format, filename);
    if (!encoder_context->format_context) {
        elv_dbg("could not allocate memory for output format");
        return eav_codec_context;
    }

    if ((rc = copy_mpegts_prepare_video_encoder(encoder_context, decoder_context, params)) != eav_success) {
        elv_err("Failure in preparing video encoder, rc=%d, url=%s", rc, params->url);
        return rc;
    }

    if ((rc = copy_mpegts_prepare_audio_encoder(encoder_context, decoder_context, params)) != eav_success) {
        elv_err("Failure in preparing audio encoder, rc=%d, url=%s", rc, params->url);
        return rc;
    }

    /*
     * Allocate an array of MAX_STREAMS out_handler_t: one for video and one for each audio output stream.
     * Needs to allocate up to number of streams when transcoding multiple streams at the same time.
     */
    out_tracker = (out_tracker_t *) calloc(1, sizeof(out_tracker_t));
    out_tracker->out_handlers = out_handlers;
    out_tracker->inctx = inctx;
    out_tracker->video_stream_index = decoder_context->video_stream_index;
    out_tracker->audio_stream_index = decoder_context->audio_stream_index[0];
    out_tracker->seg_index = atoi(params->start_segment_str);
    out_tracker->encoder_ctx = encoder_context;
    out_tracker->xc_type = xc_video;
    encoder_context->format_context->avpipe_opaque = out_tracker;

    for (int j=0; j<encoder_context->n_audio_output; j++) {
        out_tracker = (out_tracker_t *) calloc(1, sizeof(out_tracker_t));
        out_tracker->out_handlers = out_handlers;
        out_tracker->inctx = inctx;
        out_tracker->video_stream_index = decoder_context->video_stream_index;
        out_tracker->audio_stream_index = decoder_context->audio_stream_index[j];
        out_tracker->seg_index = atoi(params->start_segment_str);
        out_tracker->encoder_ctx = encoder_context;
        out_tracker->xc_type = xc_audio;
        out_tracker->output_stream_index = j;
        encoder_context->format_context2[j]->avpipe_opaque = out_tracker;
    }

    return 0;
}

static int
copy_mpegts(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    AVFrame *frame,
    AVFrame *filt_frame,
    int stream_index,
    xcparams_t *p,
    int do_instrument,
    int debug_frame_level)
{

    dump_packet(0, "COPY ", packet, 1);

    AVFormatContext *format_context;

    format_context = encoder_context->format_context;

    if (packet->pts == AV_NOPTS_VALUE ||
        packet->dts == AV_NOPTS_VALUE ||
        packet->data == NULL) {
        char *url = "";
        if (decoder_context->inctx && decoder_context->inctx->url)
            url = decoder_context->inctx->url;
        elv_warn("INVALID %s PACKET (BYPASS) url=%s pts=%"PRId64" dts=%"PRId64" duration=%"PRId64" pos=%"PRId64" size=%d stream_index=%d flags=%x data=%p\n",
            "AUDIO/VIDEO", url,
            packet->pts, packet->dts, packet->duration,
            packet->pos, packet->size, packet->stream_index,
            packet->flags, packet->data);

        // PENDING(SS) count invalid packets for stats

        return eav_success; // PENDING(SS) probably best to return an error
    }

    // Temp drop all non video
    if (packet->stream_index != 0) {
        elv_log("SSDBG AUDIO");
        return eav_success;
    }

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

    AVFrame *frame = av_frame_alloc();
    AVFrame *filt_frame = av_frame_alloc();

    while (!xctx->stop || elv_channel_size(cp_ctx->ch) > 0) {

        xc_frame = elv_channel_receive(cp_ctx->ch);
        if (!xc_frame) {
            elv_dbg("copy_mpegts_func, there is no frame, url=%s", params->url);
            continue;
        }

        AVPacket *packet = xc_frame->packet;
        if (!packet) {
            elv_err("copy_mpegts_func, packet is NULL, url=%s", params->url);
            free(xc_frame);
            continue;
        }

        dump_packet(0, "COPY IN THREAD", packet, 1);

        err = copy_mpegts(
            decoder_context,
            encoder_context,
            packet,
            frame,
            filt_frame,
            packet->stream_index,
            params,
            xctx->do_instrument,
            xctx->debug_frame_level
        );

        av_frame_unref(frame);
        av_frame_unref(filt_frame);
        av_packet_unref(packet);
        av_packet_free(&packet);
        free(xc_frame);

        if (err != eav_success) {
            elv_err("Stop video transcoding, err=%d, url=%s", err, params->url);
            break;
        }
    }

    av_frame_free(&frame);
    av_frame_free(&filt_frame);
    if (!xctx->err)
        xctx->err = err;

    elv_channel_close(xctx->vc, 0);
    elv_dbg("transcode_video_func err=%d, stop=%d, url=%s", err, xctx->stop, params->url);

    return NULL;
}
