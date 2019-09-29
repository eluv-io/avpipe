/*
 * Audio/video transcoding pipeline
 *
 * Build:
 *
 * ELV_TOOLCHAIN_DIST_PLATFORM=...
 * make
 *
 */

#include <libavutil/log.h>

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"
#include "elv_time.h"

#include <stdio.h>
#include <fcntl.h>
#include <assert.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/uio.h>
#include <unistd.h>
#include <stdlib.h>

#define AUDIO_BUF_SIZE  (128*1024)
#define INPUT_IS_SEEKABLE 0

extern int
init_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params);

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

static int
prepare_input(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    AVFormatContext *format_ctx)
{
    unsigned char *bufin;
    AVIOContext *avioctx;
    int bufin_sz = 64 * 1024;

    int avioctxBufSize = 1024 * 1024;

    bufin = (unsigned char *) av_malloc(64*1024); /* Must be malloc'd - will be realloc'd by avformat */
    avioctx = avio_alloc_context(bufin, bufin_sz, 0, (void *)inctx,
        in_handlers->avpipe_reader, in_handlers->avpipe_writer, in_handlers->avpipe_seeker);

    avioctx->written = inctx->sz; /* Fake avio_size() to avoid calling seek to find size */
    avioctx->seekable = 0;
    avioctx->direct = 0;
    avioctx->buffer_size = inctx->sz < avioctxBufSize ? inctx->sz : avioctxBufSize; // avoid seeks - avio_seek() seeks internal buffer */
    format_ctx->pb = avioctx;
    return 0;
}

static int
prepare_decoder(
    coderctx_t *decoder_context,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    txparams_t *params)
{
    int rc;
    decoder_context->last_dts = AV_NOPTS_VALUE;

    decoder_context->video_stream_index = -1;
    decoder_context->audio_stream_index = -1;

    decoder_context->format_context = avformat_alloc_context();
    if (!decoder_context->format_context) {
        elv_err("Could not allocate memory for Format Context");
        return -1;
    }

    /* Set our custom reader */
    prepare_input(in_handlers, inctx, decoder_context->format_context);

    /* Allocate AVFormatContext in format_context and find input file format */
    if (avformat_open_input(&decoder_context->format_context, "bogus.mp4", NULL, NULL) != 0) {
        elv_err("Could not open input file");
        return -1;
    }

    /* Retrieve stream information */
    if (avformat_find_stream_info(decoder_context->format_context,  NULL) < 0) {
        elv_err("Could not get input stream info");
        return -1;
    }

    dump_decoder(decoder_context);

    for (int i = 0; i < decoder_context->format_context->nb_streams; i++) {

        if (i > 1) {
            elv_err("STREAM support limited to 2");
            break;
        }

        /* Copy codec params from stream format context */
        decoder_context->codec_parameters[i] = decoder_context->format_context->streams[i]->codecpar;
        decoder_context->stream[i] = decoder_context->format_context->streams[i];

        if (decoder_context->format_context->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
            /* Video */
            decoder_context->video_stream_index = i;
            elv_dbg("STREAM %d Video, codec_id=%s", i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id));
            if (!(params->tx_type & tx_video))
                continue;

        } else if (decoder_context->format_context->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
            /* Audio */
            decoder_context->audio_stream_index = i;
            elv_dbg("STREAM %d Audio, codec_id=%s", i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id));
            if (!(params->tx_type & tx_audio))
                continue;

            /* If the buffer size is too big, ffmpeg might assert in aviobuf.c:581
             * To avoid this assertion, reset the buffer size to something smaller.
             */
            {
                AVIOContext *avioctx = (AVIOContext *)decoder_context->format_context->pb;
                if (avioctx->buffer_size > AUDIO_BUF_SIZE)
                    avioctx->buffer_size = AUDIO_BUF_SIZE;
            }
        } else {
            elv_err("STREAM UNKNOWN type=%d", decoder_context->format_context->streams[i]->codecpar->codec_type);
            continue;
        }

        /* Initialize codec and codec context */
        if (params != NULL && params->dcodec != NULL && params->dcodec[0] != '\0')
            decoder_context->codec[i] = avcodec_find_decoder_by_name(params->dcodec);
        else
            decoder_context->codec[i] = avcodec_find_decoder(decoder_context->codec_parameters[i]->codec_id);

        if (!decoder_context->codec[i]) {
            elv_err("Unsupported codec");
            return -1;
        }

        decoder_context->codec_context[i] = avcodec_alloc_context3(decoder_context->codec[i]);

        if (!decoder_context->codec_context[i]) {
            elv_err("Failed to allocated memory for AVCodecContext");
            return -1;
        }

        if (avcodec_parameters_to_context(decoder_context->codec_context[i], decoder_context->codec_parameters[i]) < 0) {
            elv_err("Failed to copy codec params to codec context");
            return -1;
        }

        /* Enable multi-threading - if thread_count is 0 the library will determine number of threads as a
         * function of the number of CPUs
         * By observation the default active_thread_type is 0 which disables multi-threading and
         * furher thread_count is 1 which forces 1 thread.
         */
        decoder_context->codec_context[i]->active_thread_type = 1;
        decoder_context->codec_context[i]->thread_count = 8;

        /* Open the decoder (initialize the decoder codec_context[i] using given codec[i]). */
        if ((rc = avcodec_open2(decoder_context->codec_context[i], decoder_context->codec[i], NULL)) < 0) {
            elv_err("Failed to open codec through avcodec_open2, err=%d, codec_id=%s", rc, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id));
            return -1;
        }

        elv_log("Input stream=%d pixel_format=%d", i, decoder_context->codec_context[i]->pix_fmt);

        /* Video - set context parameters manually */
        /* Setting the frame_rate here causes slight changes to rates - leaving it unset works perfectly
          decoder_context->codec_context[i]->framerate = av_guess_frame_rate(
            decoder_context->format_context, decoder_context->format_context->streams[i], NULL);
        */
        decoder_context->codec_context[i]->time_base = decoder_context->stream[i]->time_base;

        dump_stream(decoder_context->stream[i]);
        dump_codec_parameters(decoder_context->codec_parameters[i]);
        dump_codec_context(decoder_context->codec_context[i]);
    }

    return 0;
}

static int
prepare_video_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    txparams_t *params,
    int bypass_transcode)
{
    int rc = 0;
    int i;
    int found_pix_fmt = 0;
    int index = decoder_context->video_stream_index;

    if (index < 0) {
        elv_dbg("No video stream detected by decoder.");
        return 0;
    }

    encoder_context->video_stream_index = index;
    encoder_context->last_dts = AV_NOPTS_VALUE;
    encoder_context->stream[index] = avformat_new_stream(encoder_context->format_context, NULL);
    encoder_context->codec[index] = avcodec_find_encoder_by_name(params->ecodec);

    /* Custom output buffer */
    encoder_context->format_context->io_open = elv_io_open;
    encoder_context->format_context->io_close = elv_io_close;

    if (!encoder_context->codec[index]) {
        elv_dbg("could not find the proper codec");
        return -1;
    }
    elv_log("Found encoder %s", params->ecodec);

    if (bypass_transcode) {
        AVStream *in_stream = decoder_context->stream[index];
        AVStream *out_stream = encoder_context->stream[index];
        AVCodecParameters *in_codecpar = in_stream->codecpar;

        rc = avcodec_parameters_copy(out_stream->codecpar, in_codecpar);
        if (rc < 0) {
            elv_err("BYPASS failed to copy codec parameters\n");
            return -1;
        }

        /* Set output stream timebase when bypass encoding */
        out_stream->time_base = in_stream->time_base;
        out_stream->codecpar->codec_tag = 0;

        if (!strcmp(params->format, "fmp4"))
            av_opt_set(encoder_context->format_context->priv_data, "movflags", "frag_every_frame", 0);

        av_opt_set_int(encoder_context->format_context->priv_data, "seg_duration_ts", params->seg_duration_ts,
            AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
        av_opt_set_int(encoder_context->format_context->priv_data, "frame_duration_ts", params->frame_duration_ts,
            AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
        av_opt_set_int(encoder_context->format_context->priv_data, "start_fragment_index", params->start_fragment_index,
            AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
        av_opt_set(encoder_context->format_context->priv_data, "start_segment", params->start_segment_str, 0);
        return 0;
    }

    encoder_context->codec_context[index] = avcodec_alloc_context3(encoder_context->codec[index]);
    if (!encoder_context->codec_context[index]) {
        elv_dbg("could not allocated memory for codec context");
        return -1;
    }

    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];

    /* Set encoder parameters */
    AVDictionary *encoder_options = NULL;
#if 0
    /* These are not necessary - keeping as an example */
    // TODO: how will this be freed?
    av_opt_set(&encoder_options, "keyint", "60", 0);
    av_opt_set(&encoder_options, "min-keyint", "60", 0);
    av_opt_set(&encoder_options, "no-scenecut", "-1", 0);

    /*
     * These are not necessary in order to force keyframes for the segments
     *
    av_opt_set(encoder_codec_context->priv_data, "keyint", "60", AV_OPT_FLAG_ENCODING_PARAM);
    av_opt_set(encoder_codec_context->priv_data, "min-keyint", "60", AV_OPT_FLAG_ENCODING_PARAM);
    av_opt_set(encoder_codec_context->priv_data, "no-scenecut", "-1", AV_OPT_FLAG_ENCODING_PARAM);

    av_opt_set(encoder_codec_context->priv_data, "gop_size", "60", AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set(encoder_codec_context->priv_data, "forced_idr", "1", AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);

    // SSTEST set these on the format context as well
    av_opt_set(encoder_context->format_context->priv_data, "keyint", "60", AV_OPT_FLAG_ENCODING_PARAM);
    av_opt_set(encoder_context->format_context->priv_data, "min-keyint", "60", AV_OPT_FLAG_ENCODING_PARAM);
    av_opt_set(encoder_context->format_context->priv_data, "no-scenecut", "-1", AV_OPT_FLAG_ENCODING_PARAM);
    av_opt_set(encoder_context->format_context->priv_data, "gop_size", "60", AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set(encoder_context->format_context->priv_data, "forced_idr", "1", AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    */
#endif

    /* Added to fix/improve encoding quality of the first frame - PENDING(SSS) research */
    if ( params->crf_str && strlen(params->crf_str) > 0 )
        av_opt_set(encoder_codec_context->priv_data, "crf", params->crf_str, AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);

    // TODO: Get parameters from offering/playout
    // encoder_codec_context->level = 40;
    // encoder_codec_context->profile = FF_PROFILE_H264_HIGH;

    // Segment duration (in ts) - notice it is set on the format context not codec
    av_opt_set_int(encoder_context->format_context->priv_data, "seg_duration_ts", params->seg_duration_ts,
        AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set_int(encoder_context->format_context->priv_data, "frame_duration_ts", params->frame_duration_ts,
        AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set_int(encoder_context->format_context->priv_data, "start_fragment_index", params->start_fragment_index,
        AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set(encoder_context->format_context->priv_data, "start_segment", params->start_segment_str, 0);

    //av_opt_set(encoder_context->format_context->priv_data, "use_timeline", "0",
    //    AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);

    // Set fragmented MP4 flag if format is fmp4
    if (!strcmp(params->format, "fmp4")) {
        av_opt_set(encoder_context->format_context->priv_data, "movflags", "frag_every_frame", 0);

        // PENDING(SSS) - must use the same config as mez maker
        av_opt_set(&encoder_options, "keyint", "60", 0); // PENDING(SSS) hardcoded 50
        av_opt_set(&encoder_options, "min-keyint", "60", 0);
        av_opt_set(&encoder_options, "no-scenecut", "-1", 0);
        // av_opt_set(&encoder_options, "bframes", "0", 0);  // Hopefully not needed
    }

    /* Set codec context parameters */
    encoder_codec_context->height = params->enc_height != -1 ? params->enc_height : decoder_context->codec_context[index]->height;
    encoder_codec_context->width = params->enc_width != -1 ? params->enc_width : decoder_context->codec_context[index]->width;
    encoder_codec_context->time_base = decoder_context->codec_context[index]->time_base;
    encoder_codec_context->sample_aspect_ratio = decoder_context->codec_context[index]->sample_aspect_ratio;
    encoder_codec_context->bit_rate = params->video_bitrate;
    encoder_codec_context->rc_buffer_size = params->rc_buffer_size;
    encoder_codec_context->rc_max_rate = params->rc_max_rate;

    if (encoder_context->codec[index]->pix_fmts) {
        encoder_codec_context->pix_fmt = encoder_context->codec[index]->pix_fmts[0];
    } else {
        encoder_codec_context->pix_fmt = decoder_context->codec_context[index]->pix_fmt;
    }

    // This needs to be set before open (ffmpeg samples have it wrong)
    if (encoder_context->format_context->oformat->flags & AVFMT_GLOBALHEADER) {
        encoder_codec_context->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    }

    /* Enable hls playlist format if output format is set to "hls" */
    if (!strcmp(params->format, "hls"))
        av_opt_set(encoder_context->format_context->priv_data, "hls_playlist", "1", 0);

    /* Search for input pixel format in list of encoder pixel formats. */
    for (i=0; encoder_context->codec[index]->pix_fmts[i] >= 0; i++) {
        if (encoder_context->codec[index]->pix_fmts[i] == decoder_context->codec_context[index]->pix_fmt)
            found_pix_fmt = 1;
    }

    /* If encoder supports the input pixel format then keep it,
     * otherwise set encoder pixel format to AV_PIX_FMT_YUV420P/AV_PIX_FMT_YUV422P.
     */
    if (found_pix_fmt)
        encoder_context->codec_context[index]->pix_fmt = decoder_context->codec_context[index]->pix_fmt;
    else if (!strcmp(params->ecodec, "h264_nvenc"))
        /* If the codec is nvenc, set pixel format to AV_PIX_FMT_YUV420P */
        encoder_context->codec_context[index]->pix_fmt = AV_PIX_FMT_YUV420P;
    else
        encoder_context->codec_context[index]->pix_fmt = AV_PIX_FMT_YUV422P;

    elv_log("Output pixel_format=%d", encoder_context->codec_context[index]->pix_fmt);

    /* Open video encoder (initialize the encoder codec_context[i] using given codec[i]). */
    if ((rc = avcodec_open2(encoder_context->codec_context[index], encoder_context->codec[index], &encoder_options)) < 0) {
        elv_dbg("Could not open encoder for video, err=%d", rc);
        return -1;
    }

    /* This needs to happen after avcodec_open2() */
    if (avcodec_parameters_from_context(
            encoder_context->stream[index]->codecpar,
            encoder_context->codec_context[index]) < 0) {
        elv_dbg("could not copy encoder parameters to output stream");
        return -1;
    }

    /* Set stream parameters - after avcodec_open2 and parameters from context.
     * This is necessary for the output to preserve the timebase and framerate of the input.
     */
    encoder_context->stream[index]->time_base = decoder_context->stream[decoder_context->video_stream_index]->time_base;
    encoder_context->stream[index]->avg_frame_rate = decoder_context->stream[decoder_context->video_stream_index]->avg_frame_rate;

    return 0;
}

static int
prepare_audio_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    txparams_t *params)
{
    int index = decoder_context->audio_stream_index;

    if (index < 0) {
        elv_dbg("No audio stream detected by decoder.");
        return 0;
    }

    if (!decoder_context->codec_context[index]) {
        elv_err("Decoder codec context is NULL!\n");
        return -1;
    }

    encoder_context->audio_stream_index = index;
    encoder_context->last_dts = AV_NOPTS_VALUE;
    encoder_context->stream[index] = avformat_new_stream(encoder_context->format_context, NULL);
    encoder_context->codec[index] = avcodec_find_encoder(decoder_context->codec_context[index]->codec_id);
    if (!encoder_context->codec[index]) {
        elv_err("Codec not found, codec_id=%s\n", avcodec_get_name(decoder_context->codec_context[index]->codec_id));
        return -1;
    }

    encoder_context->format_context->io_open = elv_io_open;
    encoder_context->format_context->io_close = elv_io_close;

    encoder_context->codec_context[index] = avcodec_alloc_context3(encoder_context->codec[index]);
    //encoder_context->codec_context[index] = encoder_context->stream[index]->codec;  // Deprecated!
    encoder_context->codec_context[index]->sample_rate = decoder_context->codec_context[index]->sample_rate;
    encoder_context->codec_context[index]->time_base = decoder_context->codec_context[index]->time_base;
    encoder_context->stream[index]->time_base = encoder_context->codec_context[index]->time_base;
    if (decoder_context->codec_context[index]->sample_rate)
        encoder_context->codec_context[index]->sample_rate = decoder_context->codec_context[index]->sample_rate;
    else
        encoder_context->codec_context[index]->sample_rate = params->sample_rate;

    if (decoder_context->codec[index]->sample_fmts)
        encoder_context->codec_context[index]->sample_fmt = decoder_context->codec[index]->sample_fmts[0];
    encoder_context->codec_context[index]->channel_layout = decoder_context->codec_context[index]->channel_layout;
    encoder_context->codec_context[index]->bit_rate = params->audio_bitrate;
    encoder_context->codec_context[index]->channels = av_get_channel_layout_nb_channels(encoder_context->codec_context[index]->channel_layout);

    // Segment duration (in ts) - notice it is set on the format context not codec
    av_opt_set_int(encoder_context->format_context->priv_data, "seg_duration_ts", params->seg_duration_ts,
        AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set_int(encoder_context->format_context->priv_data, "frame_duration_ts", params->frame_duration_ts,
        AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set_int(encoder_context->format_context->priv_data, "start_fragment_index", params->start_fragment_index,
        AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    av_opt_set(encoder_context->format_context->priv_data, "start_segment", params->start_segment_str, 0);

    if (!strcmp(params->format, "fmp4")) {
        av_opt_set(encoder_context->format_context->priv_data, "movflags", "frag_every_frame", 0);
    }

    /* Open audio encoder codec */
    if (avcodec_open2(encoder_context->codec_context[index], encoder_context->codec[index], NULL) < 0) {
        elv_dbg("Could not open encoder for audio");
        return -1;
    }

    if (avcodec_parameters_from_context(
            encoder_context->stream[index]->codecpar,
            encoder_context->codec_context[index]) < 0) {
        elv_err("Failed to copy encoder parameters to output stream");
        return -1;

    }

    return 0;
}

static int
prepare_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    avpipe_io_handler_t *out_handlers,
    ioctx_t *inctx,
    txparams_t *params,
    int bypass_transcode)
{
    out_tracker_t *out_tracker;
    char *filename = "";
    char *format = params->format;

    /*
     * TODO: passing "hls" format needs some development in FF to produce stream index for audio/video.
     * I will keep hls as before to go to dashenc.c
     */
    if (!strcmp(params->format, "hls"))
        format = "dash";
    else if (!strcmp(params->format, "mp4"))
        filename = "mp4-stream.mp4";
    else if (!strcmp(params->format, "fmp4")) {
        filename = "fmp4-stream.mp4";
        /* fmp4 is actually mp4 format with a fragmented flag */
        format = "mp4";
    }

    /*
     * Allocate an AVFormatContext for output.
     * Setting 3th paramter to "dash" determines the output file format and avoids guessing
     * output file format using filename in ffmpeg library.
     */
    avformat_alloc_output_context2(&encoder_context->format_context, NULL, format, filename);
    if (!encoder_context->format_context) {
        elv_dbg("could not allocate memory for output format");
        return -1;
    }

    // Encryption applies to both audio and video
    switch (params->crypt_scheme) {
    case crypt_aes128:
        av_opt_set(encoder_context->format_context->priv_data, "hls_enc", "1",
                   0);
        if (params->crypt_iv != NULL)
            av_opt_set(encoder_context->format_context->priv_data, "hls_enc_iv",
                       params->crypt_iv, 0);
        if (params->crypt_key != NULL)
            av_opt_set(encoder_context->format_context->priv_data,
                       "hls_enc_key", params->crypt_key, 0);
        if (params->crypt_key_url != NULL)
            av_opt_set(encoder_context->format_context->priv_data,
                       "hls_enc_key_url", params->crypt_key_url, 0);
        break;
    case crypt_cenc:
        av_opt_set(encoder_context->format_context->priv_data,
                   "encryption_scheme", "cenc", 0);
        break;
    case crypt_cbc1:
        av_opt_set(encoder_context->format_context->priv_data,
                   "encryption_scheme", "cbc1", 0);
        break;
    case crypt_cens:
        av_opt_set(encoder_context->format_context->priv_data,
                   "encryption_scheme", "cens", 0);
        break;
    case crypt_cbcs:
        av_opt_set(encoder_context->format_context->priv_data,
                   "encryption_scheme", "cbcs", 0);
        break;
    case crypt_none:
        break;
    default:
        elv_err("Unimplemented crypt scheme: %d", params->crypt_scheme);
    }

    switch (params->crypt_scheme) {
    case crypt_cenc:
    case crypt_cbc1:
    case crypt_cens:
    case crypt_cbcs:
        av_opt_set(encoder_context->format_context->priv_data, "encryption_kid",
                   params->crypt_kid, 0);
        av_opt_set(encoder_context->format_context->priv_data, "encryption_key",
                   params->crypt_key, 0);
    default:
        break;
    }

    if (params->tx_type & tx_video) {
        if (prepare_video_encoder(encoder_context, decoder_context, params, bypass_transcode)) {
            elv_err("Failure in preparing video encoder");
            return -1;
        }
    }

    if (params->tx_type & tx_audio) {
        if (prepare_audio_encoder(encoder_context, decoder_context, params)) {
            elv_err("Failure in preparing audio copy");
            return -1;
        }
    }

    /* Allocate an array of 2 out_handler_t: one for video and one for audio output stream */
    out_tracker = (out_tracker_t *) calloc(2, sizeof(out_tracker_t));
    out_tracker[0].out_handlers = out_tracker[1].out_handlers = out_handlers;
    out_tracker[0].inctx = out_tracker[1].inctx = inctx;
    out_tracker[0].video_stream_index = out_tracker[1].video_stream_index = decoder_context->video_stream_index;
    out_tracker[0].audio_stream_index = out_tracker[1].audio_stream_index = decoder_context->audio_stream_index;
    out_tracker[0].seg_index = out_tracker[1].seg_index = atoi(params->start_segment_str);
    encoder_context->format_context->avpipe_opaque = out_tracker;

    dump_encoder(encoder_context);
    dump_stream(encoder_context->stream[encoder_context->video_stream_index]);
    dump_codec_context(encoder_context->codec_context[encoder_context->video_stream_index]);
    dump_stream(encoder_context->stream[encoder_context->audio_stream_index]);
    dump_codec_context(encoder_context->codec_context[encoder_context->audio_stream_index]);

    return 0;
}

static void
set_key_flag(
    AVPacket *packet,
    coderctx_t *encoder_context,
    txparams_t *params)
{
    if (packet->pts >= encoder_context->last_key_frame + params->seg_duration_ts) {
        packet->flags |= AV_PKT_FLAG_KEY;
        elv_dbg("PACKET SET KEY flag pts=%d", packet->pts);
        encoder_context->last_key_frame = packet->pts;
    }
}

static int
should_skip_encoding(
    coderctx_t *decoder_context,
    txparams_t *p,
    AVFrame *frame)
{
    int skip = 0;
    const int frame_in_pts_offset = frame->pts - decoder_context->input_start_pts;

    /* Drop frames before the desired 'start_time' */
    if (p->start_time_ts > 0 && frame_in_pts_offset < p->start_time_ts) {
        elv_dbg("ENCODE skip frame early pts=%d filt_frame pts=%d, frame_in_pts_offset=%d, start_time_ts=%d",
            frame->pts, frame_in_pts_offset, p->start_time_ts);
        skip = 1;
    }

    /* To allow for packet reordering frames can come with pts past the desired duration */
    if (p->duration_ts > 0) {
        const int max_valid_ts = p->start_time_ts + p->duration_ts;
        if (frame_in_pts_offset >= max_valid_ts) {
            elv_dbg("ENCODE skip frame late pts=%d filt_frame pts=%d, frame_in_pts_offset=%d, max_valid_ts=%d",
                frame->pts, frame_in_pts_offset, max_valid_ts);
            skip = 1;
        }
    }

    /* Skip beginning based on input packet pts */
    if (p->skip_over_pts > 0) {
        if (frame->pts <= p->skip_over_pts) {
            elv_dbg("ENCODE skip frame early pts=%d filt_frame pts=%d, frame_in_pts_offset=%d, skip_over_pts=%d",
                frame->pts, frame_in_pts_offset, p->skip_over_pts);
            skip = 1;
        }
    }
    return skip;
}

static int
encode_frame(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVFrame *frame,
    int stream_index,
    txparams_t *params,
    int debug_frame_level)
{
    int ret;
    AVFormatContext *format_context = encoder_context->format_context;
    AVCodecContext *codec_context = encoder_context->codec_context[stream_index];
    AVPacket *output_packet = av_packet_alloc();
    if (!output_packet) {
        elv_dbg("could not allocate memory for output packet");
        return -1;
    }

    ret = avcodec_send_frame(codec_context, frame);
    if (ret < 0) {
        elv_err("Failed to send frame for encoding err=%d", ret);
    }

    if (frame) {
        encoder_context->input_last_pts_sent_encode = frame->pts;
    }

    while (ret >= 0) {

        /* The packet must be initialized before receiving */
        av_init_packet(output_packet);
        output_packet->data = NULL;
        output_packet->size = 0;

        ret = avcodec_receive_packet(codec_context, output_packet);

        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
            //elv_dbg("encode_frame() EAGAIN in receiving packet");
            break;
        } else if (ret < 0) {
            elv_err("Failure while receiving a packet from the encoder: %s", av_err2str(ret));
            return -1;
        }

        /* prepare packet for muxing */
        if (params->tx_type == tx_all) {
            /* Preserve the stream index of the input */
            output_packet->stream_index = stream_index;
        } else {
            /* The output has only one stream */
            output_packet->stream_index = 0;
        }

        /*
         * Set packet duration manually if not set by the encoder.
         * The logic is borrowed from dashenc.c dash_write_packet.
         * The main problem not having a packet duration is the calculation of the track length which
         * always ends up one packet short and then all rate and duration calculcations are slightly skewed.
         * See get_cluster_duration()
         */
        assert(output_packet->duration == 0); /* Only to notice if this ever gets set */
        if (!output_packet->duration && encoder_context->last_dts != AV_NOPTS_VALUE) {
            output_packet->duration = output_packet->dts - encoder_context->last_dts;
        }
        encoder_context->last_dts = output_packet->dts;
        encoder_context->pts = output_packet->pts; // WHy was this dts?
        encoder_context->input_last_pts_encoded = output_packet->pts;

        /* Rescale using the stream time_base (not the codec context) */
        av_packet_rescale_ts(output_packet,
            decoder_context->stream[stream_index]->time_base,
            encoder_context->stream[stream_index]->time_base
        );

        set_key_flag(output_packet, encoder_context, params);

        output_packet->pts += params->start_pts;  // PENDING(SSS) Don't we have to compensate for 'relative pts'?
        output_packet->dts += params->start_pts;

        if (debug_frame_level)
            dump_packet("OUT", output_packet);

        /* mux encoded frame */
        ret = av_interleaved_write_frame(format_context, output_packet);

        if (ret != 0) {
            elv_err("Error %d while receiving a packet from the decoder: %s", ret, av_err2str(ret));
        }
    }
    av_packet_unref(output_packet);
    av_packet_free(&output_packet);
    return 0;
}

static int
transcode_packet(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    AVFrame *frame,
    AVFrame *filt_frame,
    int stream_index,
    txparams_t *p,
    int do_instrument,
    int bypass_transcode,
    int debug_frame_level)
{
    int ret;
    struct timeval tv;
    u_int64_t since;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int response;
    elv_dbg("DECODE stream_index=%d send_packet pts=%d dts=%d duration=%d", stream_index, packet->pts, packet->dts, packet->duration);

    if (bypass_transcode) {
        /*
         * The following code does an estimation of next pts and dts, but it is not accurate and
         * causes player to hang when avpipe bypasses transcoding:
         *
         *  AVStream *in_stream = decoder_context->stream[packet->stream_index];
         *  AVStream *out_stream = encoder_context->stream[packet->stream_index];
         *  packet->pts = av_rescale_q_rnd(packet->pts, in_stream->time_base, out_stream->time_base, AV_ROUND_NEAR_INF|AV_ROUND_PASS_MINMAX);
         *  packet->dts = av_rescale_q_rnd(packet->dts, in_stream->time_base, out_stream->time_base, AV_ROUND_NEAR_INF|AV_ROUND_PASS_MINMAX);
         *  packet->duration = av_rescale_q(packet->duration, in_stream->time_base, out_stream->time_base);
         *
         * This function returns frame time stamp (but needs frame):
         *  - av_frame_get_best_effort_timestamp()
         *
         * The following fields are interesting (but not initialized yet properly):
         *  - in_stream->start_time
         *  - in_stream->time_base // it always 1
         *  - codec_context->ticks_per_frame
         *
         * The following fields are valid at this point:
         *  - in_stream->avg_frame_rate.num
         *  - in_stream->avg_frame_rate.den
         *  - packet->duration
         *
         */

        set_key_flag(packet, encoder_context, p);

        packet->pts += p->start_pts;
        packet->dts += p->start_pts;

        if (debug_frame_level)
            dump_packet("BYPASS ", packet);
        if (av_interleaved_write_frame(encoder_context->format_context, packet) < 0) {
            elv_err("Failure in copying audio stream");
            return -1;
        }

        return 0;
    }

    response = avcodec_send_packet(codec_context, packet);
    if (response < 0) {
        elv_err("Failure while sending a packet to the decoder: %s", av_err2str(response));
        return response;
    }

    while (response >= 0) {
        elv_get_time(&tv);
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN) || response == AVERROR_EOF) {
            break;
        } else if (response < 0) {
            elv_err("Failure while receiving a frame from the decoder: %s", av_err2str(response));
            return response;
        }

        if (debug_frame_level)
            dump_frame("IN ", codec_context->frame_number, frame);

        if (do_instrument) {
            elv_since(&tv, &since);
            elv_log("INSTRMNT avcodec_receive_frame time=%"PRId64, since);
        }

        if (response >= 0) {
            if (codec_context->codec_type == AVMEDIA_TYPE_VIDEO) {

                decoder_context->pts = packet->pts;

                /* push the decoded frame into the filtergraph */
                elv_get_time(&tv);
                if (av_buffersrc_add_frame_flags(decoder_context->buffersrc_ctx, frame, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
                    elv_err("Failure in feeding the filtergraph");
                    break;
                }
                if (do_instrument) {
                    elv_since(&tv, &since);
                    elv_log("INSTRMNT av_buffersrc_add_frame_flags time=%"PRId64, since);
                }

                /* pull filtered frames from the filtergraph */
                while (1) {
                    elv_get_time(&tv);
                    ret = av_buffersink_get_frame(decoder_context->buffersink_ctx, filt_frame);
                    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
                        //elv_dbg("av_buffersink_get_frame() ret=EAGAIN");
                        break;
                    }

                    if (ret < 0) {
                        assert(0);
                        return -1;
                    }

                    if (do_instrument) {
                        elv_since(&tv, &since);
                        elv_log("INSTRMNT av_buffersink_get_frame time=%"PRId64, since);
                    }

#if 0
                    // TEST ONLY - save gray scale frame
                    save_gray_frame(filt_frame->data[0], filt_frame->linesize[0], filt_frame->width, filt_frame->height,
                        "frame-filt", codec_context->frame_number);
#endif

                    if (debug_frame_level)
                        dump_frame("FILT ", codec_context->frame_number, filt_frame);


                    AVFrame *frame_to_encode = filt_frame;
                    int skip = should_skip_encoding(decoder_context, p, filt_frame);

                    if (!skip) {
                        elv_get_time(&tv);
                        encode_frame(decoder_context, encoder_context, frame_to_encode, stream_index, p, debug_frame_level);
                        if (do_instrument) {
                            elv_since(&tv, &since);
                            elv_log("INSTRMNT encode_frame time=%"PRId64, since);
                        }
                    }

                    av_frame_unref(filt_frame);
                }
            }
            else {
                elv_dbg("SKIP frame codec_type=%d", codec_context->codec_type);
            }
            av_frame_unref(frame);
        }
    }
    return 0;
}

static int
flush_decoder(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    int stream_index,
    txparams_t *p,
    int debug_frame_level)
{
    int ret;
    AVFrame *frame, *filt_frame;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int response = avcodec_send_packet(codec_context, NULL);	/* Passing NULL means flush the decoder buffers */

    while (response >=0) {
        frame = av_frame_alloc();
        filt_frame = av_frame_alloc();
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN)) {
            break;
	    }

        if (response == AVERROR_EOF) {
            elv_log("1 GOT EOF");
            continue; // PENDING(SSS) why continue and not break?
        }

        if (codec_context->codec_type == AVMEDIA_TYPE_VIDEO) {

            /* Force an I frame at beginning of each segment */
            if (frame->pts % p->seg_duration_ts == 0) {
                frame->pict_type = AV_PICTURE_TYPE_I;
                elv_dbg("FRAME SET num=%d pts=%d", frame->coded_picture_number, frame->pts);
            }

            /* push the decoded frame into the filtergraph */
            if (av_buffersrc_add_frame_flags(decoder_context->buffersrc_ctx, frame, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
                elv_err("Failure in feeding the filtergraph");
                break;
            }
            /* pull filtered frames from the filtergraph */
            while (1) {
                ret = av_buffersink_get_frame(decoder_context->buffersink_ctx, filt_frame);
                if (ret == AVERROR(EAGAIN)) {
                    break;
                }

                if (ret == AVERROR_EOF) {
                    elv_log("2 GOT EOF");
                    break;
                }

                if (debug_frame_level)
                    dump_frame("FILT ", codec_context->frame_number, filt_frame);

                AVFrame *frame_to_encode = filt_frame;
                int skip = should_skip_encoding(decoder_context, p, filt_frame);

                if (!skip) {
                    encode_frame(decoder_context, encoder_context, frame_to_encode, stream_index, p, debug_frame_level);
                }
            }
        }
        av_frame_unref(filt_frame);
        av_frame_unref(frame);
    }

    return 0;
}

int
avpipe_tx(
    txctx_t *txctx,
    int do_instrument,
    int bypass_transcode,
    int debug_frame_level,
    int *last_input_pts)
{
    /* Set scale filter */
    char filter_str[128];
    struct timeval tv;
    u_int64_t since;
    coderctx_t *decoder_context = &txctx->decoder_ctx;
    coderctx_t *encoder_context = &txctx->encoder_ctx;
    txparams_t *params = txctx->params;

    if (!bypass_transcode && (params->tx_type & tx_video)) {
        sprintf(filter_str, "scale=%d:%d",
            encoder_context->codec_context[encoder_context->video_stream_index]->width,
            encoder_context->codec_context[encoder_context->video_stream_index]->height);
        elv_dbg("FILTER scale=%s", filter_str);
    }

    if (!bypass_transcode &&
        (params->tx_type & tx_video) &&
        init_filters(filter_str, decoder_context, encoder_context, txctx->params) < 0) {
        elv_err("Failed to initialize the filter");
        return -1;
    }

    if (avformat_write_header(encoder_context->format_context, NULL) < 0) {
        elv_err("Failed to open output file");
        return -1;
    }

    AVFrame *input_frame = av_frame_alloc();
    AVFrame *filt_frame = av_frame_alloc();
    if (!input_frame || !filt_frame) {
        elv_err("Failed to allocated memory for AVFrame");
        return -1;
    }

    AVPacket *input_packet = av_packet_alloc();
    if (!input_packet) {
        elv_err("Failed to allocated memory for AVPacket");
        return -1;
    }

    int response = 0;
    int rc = 0;

    elv_dbg("START TIME %d, START PTS %d (output), DURATION %d", params->start_time_ts, params->start_pts, params->duration_ts);

#if INPUT_IS_SEEKABLE
    /* Seek to start position */
    if (params->start_time_ts > 0) {
        if (av_seek_frame(decoder_context->format_context,
                decoder_context->video_stream_index, params->start_time_ts, SEEK_SET) < 0) {
            elv_err("Failed seeking to desired start frame");
            return -1;
        }
    }
#endif

    if (params->start_time_ts != -1)
        encoder_context->format_context->start_time = params->start_time_ts;
    if (params->duration_ts != -1)
        encoder_context->format_context->duration = params->duration_ts;

    if (params->seg_duration_ts % params->seg_duration_fr != 0) {
        elv_err("Frame duration is not an integer, seg_duration_ts=%d, seg_duration_fr=%d",
            params->seg_duration_ts, params->seg_duration_fr);
        return -1;
    }
    int frame_duration = params->seg_duration_ts / params->seg_duration_fr;
    int extra_pts = 5 * frame_duration; /* decode extra frames to allow for reordering */

    decoder_context->input_start_pts = -1;

    while ((rc = av_read_frame(decoder_context->format_context, input_packet)) >= 0) {

        if (decoder_context->input_start_pts == -1)
            decoder_context->input_start_pts = input_packet->pts;

        int input_packet_rel_pts = input_packet->pts - decoder_context->input_start_pts;
        // Stop when we reached the desired duration (duration -1 means 'entire input stream')
        if (params->duration_ts != -1 &&
            input_packet_rel_pts >= params->start_time_ts + params->duration_ts) {
            elv_dbg("DURATION OVER param start_time=%d duration=%d pkt pts=%d\n",
                params->start_time_ts, params->duration_ts, input_packet->pts);
            /* Allow up to 5 reoredered packets */
            if (input_packet_rel_pts >= params->start_time_ts + params->duration_ts + extra_pts) {
                elv_dbg("DURATION BREAK param start_time=%d duration=%d pkt pts=%d\n",
                    params->start_time_ts, params->duration_ts, input_packet->pts);
                break;
            }
        }

        encoder_context->input_last_pts_read = input_packet->pts;

        if (input_packet->stream_index == decoder_context->video_stream_index) {
            if (params->tx_type & tx_video) {
                // Video packet
                if (debug_frame_level)
                    dump_packet("IN ", input_packet);

                elv_get_time(&tv);
                response = transcode_packet(
                    decoder_context,
                    encoder_context,
                    input_packet,
                    input_frame,
                    filt_frame,
                    input_packet->stream_index,
                    params,
                    do_instrument,
                    bypass_transcode,
                    debug_frame_level
                );

                if (do_instrument) {
                    elv_since(&tv, &since);
                    elv_log("INSTRMNT transcode_packet time=%"PRId64, since);
                }

                av_packet_unref(input_packet);

                if (response < 0) {
                    elv_dbg("Stop transcoding, rc=%d", response);
                    break;
                }

                dump_coders(decoder_context, encoder_context);
            } else {
                elv_dbg("Skip transcoding video packet");
            }
        } else if (input_packet->stream_index == decoder_context->audio_stream_index) {
            if (params->tx_type & tx_audio) {
                // Audio packet: just copying audio stream
                av_packet_rescale_ts(input_packet,
                    decoder_context->stream[input_packet->stream_index]->time_base,
                    encoder_context->stream[input_packet->stream_index]->time_base
                );
                input_packet->pts += params->start_pts;
                input_packet->dts += params->start_pts;

                encoder_context->last_dts = input_packet->dts;

                if (debug_frame_level)
                    dump_packet("AUDIO IN", input_packet);

                if (av_interleaved_write_frame(encoder_context->format_context, input_packet) < 0) {
                    elv_err("Failure in copying audio stream, index=%d, decoder_index=%d",
                        input_packet->stream_index, decoder_context->audio_stream_index);
                    return -1;
                }
                elv_dbg("Finish copying audio packet without reencoding");
            } else {
                elv_dbg("Skip copying audio packet");
            }
        } else {
            elv_dbg("Unhandled stream - not video or audio");
        }
    }

    elv_dbg("av_read_frame() rc=%d", rc);

    if (response < 0) {
        av_packet_free(&input_packet);
        av_frame_free(&input_frame);
        return response;
    }

    /*
     * Flush all frames, first flush decoder buffers, then encoder buffers by passing NULL frame.
     * TODO: should I do it for the audio stream too?
     */
    flush_decoder(decoder_context, encoder_context, encoder_context->video_stream_index, params, debug_frame_level);
    if (!bypass_transcode && (params->tx_type & tx_video))
        encode_frame(decoder_context, encoder_context, NULL, encoder_context->video_stream_index, params, debug_frame_level);

    dump_coders(decoder_context, encoder_context);

    av_packet_free(&input_packet);
    av_frame_free(&input_frame);

    av_write_trailer(encoder_context->format_context);

    elv_log("avpipe_tx done last pts=%d input_start_pts=%d dts=%d pts_read=%d pts_sent_encode=%d pts_encoded=%d",
        encoder_context->pts, encoder_context->input_start_pts,
        encoder_context->last_dts,
        encoder_context->input_last_pts_read,
        encoder_context->input_last_pts_sent_encode,
        encoder_context->input_last_pts_encoded);

    *last_input_pts = encoder_context->input_last_pts_sent_encode;

    return 0;
}

int
avpipe_probe(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    txprobe_t **txprobe)
{
    coderctx_t decoder_ctx;
    txprobe_t *probes;
    int rc;

    if (!in_handlers) {
        elv_err("avpipe_probe NULL handlers");
        rc = -1;
        goto avpipe_probe_end;
    }

    if (prepare_decoder(&decoder_ctx, in_handlers, inctx, NULL)) {
        elv_err("avpipe_probe failed to prepare decoder");
        rc = -1;
        goto avpipe_probe_end;
    }

    int nb_streams = decoder_ctx.format_context->nb_streams;
    if (nb_streams <= 0) {
        rc = nb_streams;
        goto avpipe_probe_end;
    }

    int nb_skipped_streams = 0;
    probes = (txprobe_t *)calloc(1, sizeof(txprobe_t)*nb_streams);
    for (int i=0; i<nb_streams; i++) {
        AVStream *s = decoder_ctx.format_context->streams[i];
        AVCodecContext *codec_context = decoder_ctx.codec_context[i];
        AVCodec *codec = decoder_ctx.codec[i];

        if (codec->type != AVMEDIA_TYPE_VIDEO && codec->type != AVMEDIA_TYPE_AUDIO) {
            nb_skipped_streams++;
            continue;
        }
        probes[i].codec_type = codec->type;
        probes[i].codec_id = codec->id;
        strncpy(probes[i].codec_name, codec->name, MAX_CODEC_NAME);
        probes[i].codec_name[MAX_CODEC_NAME] = '\0';
        probes[i].duration_ts = (int)s->duration;
        probes[i].time_base = s->time_base;
        probes[i].nb_frames = s->nb_frames;
        probes[i].start_time = s->start_time;
        probes[i].avg_frame_rate = s->avg_frame_rate;
        probes[i].display_aspect_ratio = s->display_aspect_ratio;
        probes[i].frame_rate = codec_context->framerate;
        probes[i].ticks_per_frame = codec_context->ticks_per_frame;
        probes[i].bit_rate = codec_context->bit_rate;
        probes[i].has_b_frames = codec_context->has_b_frames;
        probes[i].width = codec_context->width;
        probes[i].height = codec_context->height;
        probes[i].pix_fmt = codec_context->pix_fmt;
        probes[i].sample_aspect_ratio = codec_context->sample_aspect_ratio;
        probes[i].field_order = codec_context->field_order;
    }

    *txprobe = probes;
    rc = nb_streams - nb_skipped_streams;

avpipe_probe_end:
    return rc;
}

// TODO: properly validate all params
int
avpipe_init(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    avpipe_io_handler_t *out_handlers,
    txparams_t *params,
    int bypass_transcode)
{
    txctx_t *p_txctx = (txctx_t *) calloc(1, sizeof(txctx_t));

    if (!txctx) {
        elv_err("Trancoding context is NULL");
        goto avpipe_init_failed;
    }

    if (!params) {
        elv_err("Parameters are not set");
        goto avpipe_init_failed;
    }

    if (!params->format ||
        (strcmp(params->format, "dash") &&
         strcmp(params->format, "hls") &&
         strcmp(params->format, "mp4") &&
         strcmp(params->format, "fmp4"))) {
        elv_err("Output format can be only \"dash\", \"hls\", \"mp4\", or \"fmp4\"");
        goto avpipe_init_failed;
    }

    // By default transcode 'everything'
    if (params->tx_type == tx_none) {
        params->tx_type = tx_all;
    }

    if (params->start_pts < 0) {
        elv_err("Start PTS can not be negative");
        goto avpipe_init_failed;
    }

    /* Infer rc parameters */
    if (params->video_bitrate > 0) {
        params->rc_max_rate = params->video_bitrate * 1;
        params->rc_buffer_size = params->video_bitrate;
    }

    if (prepare_decoder(&p_txctx->decoder_ctx, in_handlers, inctx, params)) {
        elv_err("Failure in preparing decoder");
        goto avpipe_init_failed;
    }

    if (prepare_encoder(&p_txctx->encoder_ctx, &p_txctx->decoder_ctx, out_handlers, inctx, params, bypass_transcode)) {
        elv_err("Failure in preparing output");
        goto avpipe_init_failed;
    }

    p_txctx->params = params;
    *txctx = p_txctx;

    char buf[1024];
    sprintf(buf,
        "format=%s "
        "start_time_ts=%d "
        "start_pts=%d "
        "duration_ts=%d "
        "start_segment_str=%s "
        "video_bitrate=%d "
        "audio_bitrate=%d "
        "sample_rate=%d "
        "crf_str=%s "
        "rc_max_rate=%d "
        "rc_buffer_size=%d "
        "seg_duration_ts=%d "
        "seg_duration_fr=%d "
        "ecodec=%s "
        "dcodec=%s "
        "enc_height=%d "
        "enc_width=%d "
        "crypt_iv=%s "
        "crypt_key=%s "
        "crypt_kid=%s "
        "crypt_key_url=%s "
        "crypt_scheme=%d ",
        params->format, params->start_time_ts, params->start_pts, params->duration_ts, params->start_segment_str,
        params->video_bitrate, params->audio_bitrate, params->sample_rate, params->crf_str,
        params->rc_max_rate, params->rc_buffer_size,
        params->seg_duration_ts, params->seg_duration_fr, params->ecodec, params->dcodec, params->enc_height, params->enc_width,
        params->crypt_iv, params->crypt_key, params->crypt_kid, params->crypt_key_url, params->crypt_scheme);
    elv_log("AVPIPE TXPARAMS %s", buf);

    return 0;

avpipe_init_failed:
    if (txctx)
        *txctx = NULL;
    free(p_txctx);
    return -1;
}

int
avpipe_fini(
    txctx_t **txctx)
{
    coderctx_t *decoder_context;
    coderctx_t *encoder_context;

    if (!txctx || !(*txctx))
        return 0;

    decoder_context = &(*txctx)->decoder_ctx;
    encoder_context = &(*txctx)->encoder_ctx;

    /* note: the internal buffer could have changed, and be != avio_ctx_buffer */
    if (decoder_context && decoder_context->format_context) {
        AVIOContext *avioctx = (AVIOContext *) decoder_context->format_context->pb;
        if (avioctx) {
            av_freep(&avioctx->buffer);
            av_freep(&avioctx);
        }
    }

    /* Corresponds to avformat_open_input */
    if (decoder_context && decoder_context->format_context)
        avformat_close_input(&decoder_context->format_context);
    //avformat_free_context(decoder_context->format_context);

    /* Free filter graph resources */
    if (decoder_context && decoder_context->filter_graph)
        avfilter_graph_free(&decoder_context->filter_graph);

    //avformat_close_input(&encoder_context->format_context);
    if (encoder_context && encoder_context->format_context)
        avformat_free_context(encoder_context->format_context);

    if (decoder_context && decoder_context->codec_context[0]) {
        /* Corresponds to avcodec_open2() */
        avcodec_close(decoder_context->codec_context[0]);
        avcodec_free_context(&decoder_context->codec_context[0]);
    }
    if (decoder_context && decoder_context->codec_context[1]) {
        /* Corresponds to avcodec_open2() */
        avcodec_close(decoder_context->codec_context[1]);
        avcodec_free_context(&decoder_context->codec_context[1]);
    }

    if (encoder_context && encoder_context->codec_context[0]) {
        /* Corresponds to avcodec_open2() */
        avcodec_close(encoder_context->codec_context[0]);
        avcodec_free_context(&encoder_context->codec_context[0]);
    }
    if (encoder_context && encoder_context->codec_context[1]) {
        /* Corresponds to avcodec_open2() */
        avcodec_close(encoder_context->codec_context[1]);
        avcodec_free_context(&encoder_context->codec_context[1]);
    }

    free(*txctx);
    *txctx = NULL;

    return 0;
}
