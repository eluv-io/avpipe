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
#include "libavutil/audio_fifo.h"
#include <libswscale/swscale.h>
#include <libavutil/imgutils.h>

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"
#include "elv_time.h"
#include "avpipe_version.h"
#include "base64.h"

#include <stdio.h>
#include <fcntl.h>
#include <assert.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/uio.h>
#include <unistd.h>
#include <stdlib.h>
#include <pthread.h>

#define AUDIO_BUF_SIZE  (128*1024)
#define INPUT_IS_SEEKABLE 0

#define MPEGTS_THREAD_COUNT     16
#define DEFAULT_THREAD_COUNT    8
#define WATERMARK_STRING_SZ     1024    /* Max length of watermark text */
#define FILTER_STRING_SZ        (1024 + WATERMARK_STRING_SZ)

extern int
init_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params);

extern int
init_overlay_filters(
    const char *filter_spec,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params);

extern int
init_audio_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params);

int
init_audio_pan_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context);

extern int
init_audio_join_filters(
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

extern const char *
av_get_pix_fmt_name(
    enum AVPixelFormat pix_fmt);

#define USE_RESAMPLE_AAC
/* This will be removed after more testing with new audio transcoding using filters */
#ifdef USE_RESAMPLE_AAC

/**
 * Initialize the audio resampler based on the input and output codec settings.
 * If the input and output sample formats differ, a conversion is required
 * libswresample takes care of this, but requires initialization.
 * @param      input_codec_context  Codec context of the input file
 * @param      output_codec_context Codec context of the output file
 * @param[out] resample_context     Resample context for the required conversion
 * @return Error code (0 if successful)
 */
static
int init_resampler(
    AVCodecContext *input_codec_context,
    AVCodecContext *output_codec_context,
    SwrContext **resample_context)
{
    int error;

    /*
     * Create a resampler context for the conversion.
     * Set the conversion parameters.
     * Default channel layouts based on the number of channels
     * are assumed for simplicity (they are sometimes not detected
     * properly by the demuxer and/or decoder).
     */
     *resample_context = swr_alloc_set_opts(NULL,
                            av_get_default_channel_layout(output_codec_context->channels),
                            output_codec_context->sample_fmt,
                            output_codec_context->sample_rate,
                            av_get_default_channel_layout(input_codec_context->channels),
                            input_codec_context->sample_fmt,
                            input_codec_context->sample_rate,
                            0, NULL);
    if (!*resample_context) {
        elv_err("Could not allocate resample context");
        return AVERROR(ENOMEM);
    }
    /*
     * Perform a sanity check so that the number of converted samples is
     * not greater than the number of samples to be converted.
     * If the sample rates differ, this case has to be handled differently
     */
    if (output_codec_context->sample_rate != input_codec_context->sample_rate) {
        elv_err("Output sample_rate (%d) doesn't match input sample_rate (%d)",
            output_codec_context->sample_rate, input_codec_context->sample_rate);
            return AVERROR(EINVAL);
    }

    /* Open the resampler with the specified parameters. */
    if ((error = swr_init(*resample_context)) < 0) {
        elv_err("Could not open resample context, error=%d", error);
        swr_free(resample_context);
        return error;
    }

    return 0;
}

/**
 * Initialize a temporary storage for the specified number of audio samples.
 * The conversion requires temporary storage due to the different format.
 * The number of audio samples to be allocated is specified in frame_size.
 * @param[out] converted_input_samples Array of converted samples. The
 *                                     dimensions are reference, channel
 *                                     (for multi-channel audio), sample.
 * @param      output_codec_context    Codec context of the output file
 * @param      frame_size              Number of samples to be converted in
 *                                     each round
 * @return Error code (0 if successful)
 */
static
int init_converted_samples(
    uint8_t ***converted_input_samples,
    AVCodecContext *output_codec_context,
    int frame_size)
{
    int error;

    /* Allocate as many pointers as there are audio channels.
     * Each pointer will later point to the audio samples of the corresponding
     * channels (although it may be NULL for interleaved formats).
     */
    if (!(*converted_input_samples = calloc(output_codec_context->channels,
                                            sizeof(**converted_input_samples)))) {
        elv_err("Could not allocate converted input sample pointers");
        return AVERROR(ENOMEM);
    }

    /* Allocate memory for the samples of all channels in one consecutive
     * block for convenience. */
    if ((error = av_samples_alloc(*converted_input_samples, NULL,
                                  output_codec_context->channels,
                                  frame_size,
                                  output_codec_context->sample_fmt, 0)) < 0) {
        elv_err("Could not allocate converted input samples, error='%s'", av_err2str(error));
        av_freep(&(*converted_input_samples)[0]);
        free(*converted_input_samples);
        return error;
    }
    return 0;
}

/**
 * Convert the input audio samples into the output sample format.
 * The conversion happens on a per-frame basis, the size of which is
 * specified by frame_size.
 * @param      input_data       Samples to be decoded. The dimensions are
 *                              channel (for multi-channel audio), sample.
 * @param[out] converted_data   Converted samples. The dimensions are channel
 *                              (for multi-channel audio), sample.
 * @param      frame_size       Number of samples to be converted
 * @param      resample_context Resample context for the conversion
 * @return Error code (0 if successful)
 */
static
int convert_samples(
    const uint8_t **input_data,
    uint8_t **converted_data,
    const int frame_size,
    SwrContext *resample_context)
{
    int error;

    /* Convert the samples using the resampler. */
    if ((error = swr_convert(resample_context,
                             converted_data, frame_size,
                             input_data    , frame_size)) < 0) {
        elv_err("Could not convert input samples, error='%s'", av_err2str(error));
        return error;
    }

    return 0;
}

static int init_output_frame(AVFrame **frame,
                             AVCodecContext *output_codec_context,
                             int frame_size)
{
    int error;

    /* Create a new frame to store the audio samples. */
    if (!(*frame = av_frame_alloc())) {
        elv_err("Failed to allocate output frame");
        return AVERROR_EXIT;
    }

    /* Set the frame's parameters, especially its size and format.
     * av_frame_get_buffer needs this to allocate memory for the
     * audio samples of the frame.
     * Default channel layouts based on the number of channels
     * are assumed for simplicity. */
    (*frame)->nb_samples     = frame_size;
    (*frame)->channel_layout = output_codec_context->channel_layout;
    (*frame)->format         = output_codec_context->sample_fmt;
    (*frame)->sample_rate    = output_codec_context->sample_rate;

    /* Allocate the samples of the created frame. This call will make
     * sure that the audio frame can hold as many samples as specified. */
    if ((error = av_frame_get_buffer(*frame, 0)) < 0) {
        elv_err("Failed to allocate output frame samples (error '%s')",
                av_err2str(error));
        av_frame_free(frame);
        return error;
    }

    return 0;
}

#endif

int
prepare_input(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    coderctx_t *decoder_context,
    int seekable)
{
    unsigned char *bufin;
    AVIOContext *avioctx;
    int bufin_sz = 1024 * 1024;

    /* For RTMP protocol don't create input callbacks */
    decoder_context->is_rtmp = 0;
    if (inctx->url && !strncmp(inctx->url, "rtmp", 4)) {
        decoder_context->is_rtmp = 1;
        return 0;
    }

    bufin = (unsigned char *) av_malloc(bufin_sz);  /* Must be malloc'd - will be realloc'd by avformat */
    avioctx = avio_alloc_context(bufin, bufin_sz, 0, (void *)inctx,
        in_handlers->avpipe_reader, in_handlers->avpipe_writer, in_handlers->avpipe_seeker);

    avioctx->written = inctx->sz; /* Fake avio_size() to avoid calling seek to find size */
    avioctx->seekable = seekable;
    avioctx->direct = 0;
    avioctx->buffer_size = inctx->sz < bufin_sz ? inctx->sz : bufin_sz; // avoid seeks - avio_seek() seeks internal buffer */
    decoder_context->format_context->pb = avioctx;
    return 0;
}

#define TIMEBASE_THRESHOLD  10000

static int
calc_timebase(
    int timebase)
{
    if (timebase <= 0) {
        elv_err("calc_timebase invalid timebase=%d", timebase);
        return timebase;
    }

    while (timebase < TIMEBASE_THRESHOLD)
        timebase *= 2;

    return timebase;
}

static int
selected_audio_index(
    txparams_t *params,
    int index)
{
    if (params->n_audio <= 0)
        return -1;

    for (int i=0; i<params->n_audio; i++) {
        if (params->audio_index[i] == index)
            return i;
    }

    return -1;
}

static int
selected_decoded_audio(
    coderctx_t *decoder_context,
    int index)
{
    if (decoder_context->n_audio <= 0)
        return -1;

    for (int i=0; i<decoder_context->n_audio; i++) {
        if (decoder_context->audio_stream_index[i] == index)
            return i;
    }

    return -1;
}

static int
prepare_decoder(
    coderctx_t *decoder_context,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    txparams_t *params,
    int seekable)
{
    int rc;
    decoder_context->video_last_dts = AV_NOPTS_VALUE;
    decoder_context->audio_last_dts = AV_NOPTS_VALUE;
    int stream_id_index = -1;
    int sync_id_index = -1;     // Index of the video stream used for audio sync

    decoder_context->video_stream_index = -1;
    for (int j=0; j<MAX_AUDIO_MUX; j++)
        decoder_context->audio_stream_index[j] = -1;

    decoder_context->format_context = avformat_alloc_context();
    if (!decoder_context->format_context) {
        elv_err("Could not allocate memory for Format Context");
        return -1;
    }

    /* Set global decoding options */
    /* Disable timestamp wrapping */
    decoder_context->format_context->correct_ts_overflow = 0;

    /* Set our custom reader */
    prepare_input(in_handlers, inctx, decoder_context, seekable);

    AVDictionary *opts = NULL;
    if (params && params->listen)
        av_dict_set(&opts, "listen", "1" , 0);
    /* Allocate AVFormatContext in format_context and find input file format */
    rc = avformat_open_input(&decoder_context->format_context, inctx->url, NULL, &opts);
    if (rc != 0) {
        elv_err("Could not open input file, err=%d", rc);
        return -1;
    }

    /* Retrieve stream information */
    if (avformat_find_stream_info(decoder_context->format_context,  NULL) < 0) {
        elv_err("Could not get input stream info");
        return -1;
    }

    decoder_context->is_mpegts = 0;
    if (decoder_context->format_context->iformat &&
        decoder_context->format_context->iformat->name &&
        !strcmp(decoder_context->format_context->iformat->name, "mpegts")) {
        decoder_context->is_mpegts = 1;
    }

    for (int i = 0; i < decoder_context->format_context->nb_streams && i < MAX_STREAMS; i++) {

        switch (decoder_context->format_context->streams[i]->codecpar->codec_type) {
        case AVMEDIA_TYPE_VIDEO:
            /* Video, copy codec params from stream format context */
            decoder_context->codec_parameters[i] = decoder_context->format_context->streams[i]->codecpar;
            decoder_context->stream[i] = decoder_context->format_context->streams[i];

            /* If no stream ID specified - choose the first video stream encountered */
            if (params && params->stream_id < 0 && decoder_context->video_stream_index < 0)
                decoder_context->video_stream_index = i;
            elv_dbg("VIDEO STREAM %d, codec_id=%s, stream_id=%d, timebase=%d, tx_type=%d",
                i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id), decoder_context->stream[i]->id,
                decoder_context->stream[i]->time_base.den, params ? params->tx_type : tx_none);
            break;

        case AVMEDIA_TYPE_AUDIO:
            /* Audio, copy codec params from stream format context */
            decoder_context->codec_parameters[i] = decoder_context->format_context->streams[i]->codecpar;
            decoder_context->stream[i] = decoder_context->format_context->streams[i];

            /* If no stream ID specified - choose the first audio stream encountered */
            if (params && params->stream_id < 0 &&
                (selected_audio_index(params, i) >= 0 || (params->n_audio == 0 && decoder_context->n_audio == 0))) {
                decoder_context->audio_stream_index[decoder_context->n_audio] = i;
                decoder_context->n_audio++;
            }
            elv_dbg("AUDIO STREAM %d, codec_id=%s, stream_id=%d, timebase=%d, tx_type=%d",
                i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id), decoder_context->stream[i]->id,
                decoder_context->stream[i]->time_base.den, params ? params->tx_type : tx_none);

            /* If the buffer size is too big, ffmpeg might assert in aviobuf.c:581
             * To avoid this assertion, reset the buffer size to something smaller.
             */
            {
                AVIOContext *avioctx = (AVIOContext *)decoder_context->format_context->pb;
                if (avioctx->buffer_size > AUDIO_BUF_SIZE)
                    avioctx->buffer_size = AUDIO_BUF_SIZE;
            }
            break;

        case AVMEDIA_TYPE_DATA:
            decoder_context->codec_parameters[i] = decoder_context->format_context->streams[i]->codecpar;
            decoder_context->stream[i] = decoder_context->format_context->streams[i];
            decoder_context->data_stream_index = i;
            elv_dbg("DATA STREAM %d, codec_id=%s, stream_id=%d",
                i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id), decoder_context->stream[i]->id);
            break;

        default:
            decoder_context->codec[i] = NULL;
            elv_dbg("UNKNOWN STREAM type=%d", decoder_context->format_context->streams[i]->codecpar->codec_type);
            continue;
        }

        /* Is this the stream selected for transcoding? */
        int selected_stream = 0;
        if (params && decoder_context->stream[i]->id == params->stream_id) {
            elv_log("STREAM MATCH stream_id=%d stream_index=%d tx_type=%d", params->stream_id, i, params->tx_type);
            stream_id_index = i;
            selected_stream = 1;
        }

        if (params && decoder_context->stream[i]->id == params->sync_audio_to_stream_id) {
            if (decoder_context->stream[i]->codecpar->codec_type != AVMEDIA_TYPE_VIDEO) {
                elv_err("Syncing to non-video stream is not possible, sync_audio_to_stream_id=%d", params->sync_audio_to_stream_id);
                return -1;
            }
            elv_log("STREAM MATCH sync_stream_id=%d stream_index=%d", params->sync_audio_to_stream_id, i);
            sync_id_index = i;
        }

        /* If stream ID is not set - match audio_index */
        if (params && params->stream_id < 0 &&
            params->tx_type & tx_audio &&
            selected_audio_index(params, i) >= 0) {
            selected_stream = 1;
        }

        /*
         * Find decoder and initialize decoder context.
         * Pick params->decodec if this is the selected stream (stream_id or audio_index)
         */
        if (params != NULL && params->dcodec != NULL && params->dcodec[0] != '\0' && selected_stream &&
            decoder_context->format_context->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
            elv_log("STREAM SELECTED id=%d idx=%d tx_type=%d dcodec=%s", decoder_context->stream[i]->id,
                    i, params->tx_type, params->dcodec);
            decoder_context->codec[i] = avcodec_find_decoder_by_name(params->dcodec);
        } else if (params != NULL && params->dcodec2 != NULL && params->dcodec2[0] != '\0' && selected_stream &&
            decoder_context->format_context->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
            elv_log("STREAM SELECTED id=%d idx=%d tx_type=%d dcodec2=%s", decoder_context->stream[i]->id,
                    i, params->tx_type, params->dcodec2);
            decoder_context->codec[i] = avcodec_find_decoder_by_name(params->dcodec2);
        } else {
            decoder_context->codec[i] = avcodec_find_decoder(decoder_context->codec_parameters[i]->codec_id);
        }

        if (decoder_context->codec_parameters[i]->codec_type != AVMEDIA_TYPE_DATA && !decoder_context->codec[i]) {
            elv_err("Unsupported decoder codec param=%s, codec_id=%d",
                params ? params->dcodec : "", decoder_context->codec_parameters[i]->codec_id);
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
        if (decoder_context->is_mpegts || decoder_context->is_rtmp)
            decoder_context->codec_context[i]->thread_count = MPEGTS_THREAD_COUNT;
        else
            decoder_context->codec_context[i]->thread_count = DEFAULT_THREAD_COUNT;

        /* Open the decoder (initialize the decoder codec_context[i] using given codec[i]). */
        if (decoder_context->codec_parameters[i]->codec_type != AVMEDIA_TYPE_DATA &&
             (rc = avcodec_open2(decoder_context->codec_context[i], decoder_context->codec[i], NULL)) < 0) {
            elv_err("Failed to open codec through avcodec_open2, err=%d, param=%s, codec_id=%s",
                rc, params->dcodec, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id));
            return -1;
        }

        elv_log("Input stream=%d pixel_format=%s, timebase=%d, sample_fmt=%d, frame_size=%d",
            i,
            av_get_pix_fmt_name(decoder_context->codec_context[i]->pix_fmt),
            decoder_context->stream[i]->time_base.den,
            decoder_context->codec_context[i]->sample_fmt,
            decoder_context->codec_context[i]->frame_size);

        /* Video - set context parameters manually */
        /* Setting the frame_rate here causes slight changes to rates - leaving it unset works perfectly
          decoder_context->codec_context[i]->framerate = av_guess_frame_rate(
            decoder_context->format_context, decoder_context->format_context->streams[i], NULL);
        */

        /*
         * NOTE: Don't change the input stream or input codec_context timebase to 1/sample_rate.
         * This will break transcoding audio if the input timebase doesn't match with output timebase. (-RM)
         */
        decoder_context->codec_context[i]->time_base = decoder_context->stream[i]->time_base;

        dump_stream(decoder_context->stream[i]);
        dump_codec_parameters(decoder_context->codec_parameters[i]);
        dump_codec_context(decoder_context->codec_context[i]);
    }

    /* If it couldn't find identified stream with params->stream_id, then return an error */
    if (params && params->stream_id >= 0 && stream_id_index < 0) {
        elv_err("Invalid stream_id=%d", params->stream_id);
        return -1;
    }

    if (stream_id_index >= 0) {
        if (decoder_context->format_context->streams[stream_id_index]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
            decoder_context->video_stream_index = stream_id_index;
            params->tx_type = tx_video;
        } else if (decoder_context->format_context->streams[stream_id_index]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
            decoder_context->audio_stream_index[decoder_context->n_audio] = stream_id_index;
            decoder_context->n_audio++;
            params->tx_type = tx_audio;

            if (sync_id_index >= 0)
                decoder_context->video_stream_index = sync_id_index;
        }
    } else if (params && (params->n_audio == 1) &&
            (params->audio_index[0] < decoder_context->format_context->nb_streams)) {
        decoder_context->audio_stream_index[0] = params->audio_index[0];
        decoder_context->n_audio = 1;

    }
    elv_dbg("prepare_decoder tx_type=%d, video_stream_index=%d audio_stream_index=%d, n_audio=%d, nb_streams=%d",
        params ? params->tx_type : 0,
        decoder_context->video_stream_index,
        decoder_context->audio_stream_index[0],
        decoder_context->n_audio,
        decoder_context->format_context->nb_streams);

    dump_decoder(inctx->url, decoder_context);

    /*
     * If params->force_equal_fduration is set, then initialize decoder_context->frame_duration.
     * This only applies to transcoding frames with format fmp4-segment (creating mezzanines).
     * If decoder_context->frame_duration is initialized (>0), then all the decoded frames would have
     * a frame duration equal to decoder_context->frame_duration before filtering/encoding.
     * (This actually changes the PTS of a decoded frame so that the frame has a distance equal to
     * decoder_context->frame_duration from previous frame)
     * For video we have to become sure the calculation has no decimal, otherwise the video and audio would drift.
     */
    if (params && params->force_equal_fduration && !strcmp(params->format, "fmp4-segment")) {
        if (params->tx_type & tx_video) {
            if (decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num == 0 ||
            (decoder_context->stream[decoder_context->video_stream_index]->time_base.den *
            decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.den %
            decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num != 0)) {
                elv_err("Can not force equal frame duration, timebase=%d, frame_rate=%d/%d",
                    decoder_context->stream[decoder_context->video_stream_index]->time_base.den,
                    decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.den,
                    decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num);
                return -1;
            }
            decoder_context->frame_duration = decoder_context->stream[decoder_context->video_stream_index]->time_base.den *
                decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.den /
                decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num;
            elv_dbg("SET VIDEO FRAME DURATION, timebase=%d, frame_rate.den=%d, frame_rate.num=%d, duration=%d",
                decoder_context->stream[decoder_context->video_stream_index]->time_base.den,
                decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.den,
                decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num,
                decoder_context->frame_duration);
        }
        /* PENDING(RM) Do we need this for audio? Because audio is based on resampling, it doesn't work like video. */
    }

    return 0;
}

static int
set_encoder_options(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    txparams_t *params,
    int stream_index,
    int timebase)
{
    if (timebase <= 0) {
        elv_err("Setting encoder options failed, invalid timebase=%d (check encoding params)", timebase);
        return -1;
    }

    if (!strcmp(params->format, "fmp4")) {
        if (stream_index == decoder_context->video_stream_index)
            av_opt_set(encoder_context->format_context->priv_data, "movflags", "frag_every_frame", 0);
        if (selected_decoded_audio(decoder_context, stream_index) >= 0)
            av_opt_set(encoder_context->format_context2->priv_data, "movflags", "frag_every_frame", 0);
    }

    // Segment duration (in ts) - notice it is set on the format context not codec
    if (params->audio_seg_duration_ts > 0 && (!strcmp(params->format, "dash") || !strcmp(params->format, "hls"))) {
        if (selected_decoded_audio(decoder_context, stream_index) >= 0)
            av_opt_set_int(encoder_context->format_context2->priv_data, "seg_duration_ts", params->audio_seg_duration_ts,
                AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    }

    if (params->video_seg_duration_ts > 0 && (!strcmp(params->format, "dash") || !strcmp(params->format, "hls"))) {
        if (stream_index == decoder_context->video_stream_index)
            av_opt_set_int(encoder_context->format_context->priv_data, "seg_duration_ts", params->video_seg_duration_ts,
                AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    }

    if (selected_decoded_audio(decoder_context, stream_index) >= 0) {
        if (!(params->tx_type & tx_audio)) {
            elv_err("Failed to set encoder options, stream_index=%d, tx_type=%d", stream_index, params->tx_type);
            return -1;
        }
        av_opt_set_int(encoder_context->format_context2->priv_data, "start_fragment_index", params->start_fragment_index,
            AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
        av_opt_set(encoder_context->format_context2->priv_data, "start_segment", params->start_segment_str, 0);
    }

    if (stream_index == decoder_context->video_stream_index) {
        if (!(params->tx_type & tx_video)) {
            elv_err("Failed to set encoder options, stream_index=%d, tx_type=%d", stream_index, params->tx_type);
            return -1;
        }
        av_opt_set_int(encoder_context->format_context->priv_data, "start_fragment_index", params->start_fragment_index,
            AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
        av_opt_set(encoder_context->format_context->priv_data, "start_segment", params->start_segment_str, 0);
    }

    if (!strcmp(params->format, "fmp4-segment") || !strcmp(params->format, "segment")) {
        int64_t seg_duration_ts = 0;
        float seg_duration = 0;
        /* Precalculate seg_duration_ts based on seg_duration if seg_duration is set */
        if (params->seg_duration) {
            seg_duration = atof(params->seg_duration);
            if (stream_index == decoder_context->video_stream_index)
                timebase = calc_timebase(timebase);
            seg_duration_ts = seg_duration * timebase;
        }
        if (selected_decoded_audio(decoder_context, stream_index) >= 0) {
            if (params->audio_seg_duration_ts > 0)
                seg_duration_ts = params->audio_seg_duration_ts;
            av_opt_set_int(encoder_context->format_context2->priv_data, "segment_duration_ts", seg_duration_ts, 0);
            /* If audio_seg_duration_ts is not set, set it now */
            if (params->audio_seg_duration_ts <= 0)
                params->audio_seg_duration_ts = seg_duration_ts;
            elv_dbg("setting \"fmp4-segment\" audio segment_time to %s, seg_duration_ts=%"PRId64, params->seg_duration, seg_duration_ts);
            av_opt_set(encoder_context->format_context2->priv_data, "reset_timestamps", "on", 0);
        } 
        if (stream_index == decoder_context->video_stream_index) {
            if (params->video_seg_duration_ts > 0)
                seg_duration_ts = params->video_seg_duration_ts;
            av_opt_set_int(encoder_context->format_context->priv_data, "segment_duration_ts", seg_duration_ts, 0);
            /* If video_seg_duration_ts is not set, set it now */
            if (params->video_seg_duration_ts <= 0)
                params->video_seg_duration_ts = seg_duration_ts;
            elv_dbg("setting \"fmp4-segment\" video segment_time to %s, seg_duration_ts=%"PRId64, params->seg_duration, seg_duration_ts);
            av_opt_set(encoder_context->format_context->priv_data, "reset_timestamps", "on", 0);
        }
        // If I set faststart in the flags then ffmpeg generates some zero size files, which I need to dig into it more (RM).
        // av_opt_set(encoder_context->format_context->priv_data, "segment_format_options", "movflags=faststart", 0);
        // So lets use flag_every_frame option instead.
        if (!strcmp(params->format, "fmp4-segment")) {
            if (selected_decoded_audio(decoder_context, stream_index) >= 0)
                av_opt_set(encoder_context->format_context2->priv_data, "segment_format_options", "movflags=frag_every_frame", 0);
            if (stream_index == decoder_context->video_stream_index)
                av_opt_set(encoder_context->format_context->priv_data, "segment_format_options", "movflags=frag_every_frame", 0);
        }
    }

    return 0;
}

static int
find_level(
    int width,
    int height)
{
    int level;

    /*
     * Reference: https://en.wikipedia.org/wiki/Advanced_Video_Coding
     */
    if (height <= 480)
        level = 30;
    else if (height <= 720)
        level = 31;
    else if (height <= 1080)
        level = 42;
    else if (height <= 1920)
        level = 50;
    else if (height <= 2048)
        level = 51;
    else
        level = 52;

    if (level < 31 && width >= 720)
        level = 31;

    if (level < 42 && width >= 1280 && height >= 720)
        level = 42;

    if (level < 50 && width >= 1920 && height >= 1080)
        level = 50;

    if (level < 52 && width >= 2560 && height >= 1920)
        level = 52;

    if (level < 60 && width >= 3840 && height >= 2160)
        level = 60;

    return level;
}

/*
 * Set H264 specific params profile, and level based on encoding height.
 */
static void
set_h264_params(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    txparams_t *params)
{
    int index = decoder_context->video_stream_index;
    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];

    /* Codec level and profile must be set correctly per H264 spec */
    if (encoder_codec_context->height <= 480)
        /*
         * FF_PROFILE_H264_BASELINE is primarily for lower-cost applications with limited computing resources,
         * this profile is used widely in videoconferencing and mobile applications.
         */
        encoder_codec_context->profile = FF_PROFILE_H264_BASELINE;
    else
        /*
         * FF_PROFILE_H264_HIGH is the primary profile for broadcast and disc storage applications,
         * particularly for high-definition television applications.
         */
        encoder_codec_context->profile = FF_PROFILE_H264_HIGH;

    /*
     * These are set according to
     * https://en.wikipedia.org/wiki/Advanced_Video_Coding#Levels
     * https://developer.apple.com/documentation/http_live_streaming/hls_authoring_specification_for_apple_devices
     */
    encoder_codec_context->level = find_level(encoder_codec_context->width, encoder_codec_context->height);
}

static void
set_h265_params(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    txparams_t *params)
{
    int index = decoder_context->video_stream_index;
    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];

    /*
     * The Main profile allows for a bit depth of 8-bits per sample with 4:2:0 chroma sampling,
     * which is the most common type of video used with consumer devices
     * For HDR10 we need MAIN 10 that supports 10 bit profile.
     */
    if (params->bitdepth == 8)
        av_opt_set(encoder_codec_context->priv_data, "profile", "main", 0);
    else {
        av_opt_set(encoder_codec_context->priv_data, "profile", "main10", 0);
        av_opt_set(encoder_codec_context->priv_data, "x265-params", "hdr-opt=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc", 0);
    }

    /* Set max_cll and master_display meta data for HDR content */
    if (params->max_cll && params->max_cll[0] != '\0')
        av_opt_set(encoder_codec_context->priv_data, "max-cll", params->max_cll, 0);
    if (params->master_display && params->master_display[0] != '\0')
        av_opt_set(encoder_codec_context->priv_data, "master-display", params->master_display, 0);

    /* Set the number of bframes to 0 and avoid having bframes */
    av_opt_set_int(encoder_codec_context->priv_data, "bframes", 0, 0);

    /*
     * These are set according to
     * https://en.wikipedia.org/wiki/High_Efficiency_Video_Coding
     */
    encoder_codec_context->level = find_level(encoder_codec_context->width, encoder_codec_context->height);
}

/* Borrowed from libavcodec/nvenc.h since it is not exposed */
enum {
    NV_ENC_H264_PROFILE_BASELINE,
    NV_ENC_H264_PROFILE_MAIN,
    NV_ENC_H264_PROFILE_HIGH,
    NV_ENC_H264_PROFILE_HIGH_444P,
};

enum {
    PRESET_DEFAULT = 0,
    PRESET_SLOW,
    PRESET_MEDIUM,
    PRESET_FAST,
    PRESET_HP,
    PRESET_HQ,
    PRESET_BD ,
    PRESET_LOW_LATENCY_DEFAULT ,
    PRESET_LOW_LATENCY_HQ ,
    PRESET_LOW_LATENCY_HP,
    PRESET_LOSSLESS_DEFAULT, // lossless presets must be the last ones
    PRESET_LOSSLESS_HP,
};

static void
set_nvidia_params(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    txparams_t *params)
{
    int index = decoder_context->video_stream_index;
    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];

    av_opt_set(encoder_codec_context->priv_data, "forced-idr", "on", 0);

    if (params->gpu_index >= 0)
        av_opt_set_int(encoder_codec_context->priv_data, "gpu", params->gpu_index, 0);

    /*
     * The encoder_codec_context->profile is set just for proper log message, otherwise it has no impact
     * when the encoder is nvidia.
     */
    if (encoder_codec_context->height <= 480) {
        av_opt_set_int(encoder_codec_context->priv_data, "profile", NV_ENC_H264_PROFILE_BASELINE, 0);
        encoder_codec_context->profile = FF_PROFILE_H264_BASELINE;
    } else {
        av_opt_set_int(encoder_codec_context->priv_data, "profile", NV_ENC_H264_PROFILE_HIGH, 0);
        encoder_codec_context->profile = FF_PROFILE_H264_HIGH;
    }

    encoder_codec_context->level = find_level(encoder_codec_context->width, encoder_codec_context->height);
    av_opt_set_int(encoder_codec_context->priv_data, "level", encoder_codec_context->level, 0);

    /*
     * According to https://superuser.com/questions/1296374/best-settings-for-ffmpeg-with-nvenc
     * the best setting can be PRESET_LOW_LATENCY_HQ or PRESET_LOW_LATENCY_HP.
     * (in my experiment PRESET_LOW_LATENCY_HQ is better than PRESET_LOW_LATENCY_HP)
     */
    av_opt_set_int(encoder_codec_context->priv_data, "preset", PRESET_LOW_LATENCY_HQ, 0);

    /*
     * We might want to set one of the following options in future:
     *
     * av_opt_set(encoder_codec_context->priv_data, "bluray-compat", "on", 0);
     * av_opt_set(encoder_codec_context->priv_data, "strict_gop", "on", 0);
     * av_opt_set(encoder_codec_context->priv_data, "b_adapt", "off", 0);
     * av_opt_set(encoder_codec_context->priv_data, "nonref_p", "on", 0);
     * av_opt_set(encoder_codec_context->priv_data, "2pass", "on", 0);
     *
     * Constant quantization parameter rate control method
     * av_opt_set_int(encoder_codec_context->priv_data, "qp", 15, 0);
     * av_opt_set_int(encoder_codec_context->priv_data, "init_qpB", 20, 0);
     * av_opt_set_int(encoder_codec_context->priv_data, "init_qpP", 20, 0);
     * av_opt_set_int(encoder_codec_context->priv_data, "init_qpI", 20, 0);

     * char level[10];
     * sprintf(level, "%d.", 15);
     * av_opt_set(encoder_codec_context->priv_data, "cq", level, 0);
     */
}

static int
set_pixel_fmt(
    AVCodecContext *encoder_codec_context,
    txparams_t *params)
{
    /* Using the spec in https://en.wikipedia.org/wiki/High_Efficiency_Video_Coding */
    switch (params->bitdepth) {
    case 8:
        encoder_codec_context->pix_fmt = AV_PIX_FMT_YUV420P;
        break;
    case 10:
        /* AV_PIX_FMT_YUV420P10LE: 15bpp, (1 Cr & Cb sample per 2x2 Y samples), little-endian.
         * If encoder is h265 then AV_PIX_FMT_YUV420P10LE matches with MAIN10 profile (V1).
         */
        encoder_codec_context->pix_fmt = AV_PIX_FMT_YUV420P10LE;
        break;
    case 12:
        /* AV_PIX_FMT_YUV422P12LE: 24bpp, (1 Cr & Cb sample per 2x1 Y samples), little-endian */
        encoder_codec_context->pix_fmt = AV_PIX_FMT_YUV422P12LE;
        break;
    default:
        elv_err("Invalid bitdepth=%d", params->bitdepth);
        return -1;
    }

    return 0;
}

static int
prepare_video_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    txparams_t *params)
{
    int rc = 0;
    int index = decoder_context->video_stream_index;

    if (index < 0) {
        elv_dbg("No video stream detected by decoder.");
        return 0;
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
        return -1;
    }
    elv_log("Found encoder index=%d, %s", index, params->ecodec);

    if (params->bypass_transcoding) {
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

        rc = set_encoder_options(encoder_context, decoder_context, params, decoder_context->video_stream_index,
            decoder_context->stream[decoder_context->video_stream_index]->time_base.den);
        if (rc < 0) {
            elv_err("Failed to set video encoder options with bypass");
            return -1;
        }
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

    /* Added to fix/improve encoding quality of the first frame - PENDING(SSS) research */
    if ( params->crf_str && strlen(params->crf_str) > 0 ) {
        /* The 'crf' option may be overriden by rate control options - 'crf_max' is used as a safety net */
        av_opt_set(encoder_codec_context->priv_data, "crf", params->crf_str, AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
        // av_opt_set(encoder_codec_context->priv_data, "crf_max", params->crf_str, AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    }

    if (params->preset && strlen(params->preset) > 0 &&
        (!strcmp(params->format, "fmp4-segment") || !strcmp(params->format, "fmp4"))) {
        av_opt_set(encoder_codec_context->priv_data, "preset", params->preset, AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    }

    if (!strcmp(params->format, "fmp4-segment") || !strcmp(params->format, "fmp4")) {
        encoder_codec_context->max_b_frames = 0;
    }

    if (params->force_keyint > 0) {
        encoder_codec_context->gop_size = params->force_keyint;
    }

    encoder_context->stream[encoder_context->video_stream_index]->time_base = decoder_context->codec_context[index]->time_base;
    rc = set_encoder_options(encoder_context, decoder_context, params, decoder_context->video_stream_index,
        decoder_context->stream[decoder_context->video_stream_index]->time_base.den);
    if (rc < 0) {
        elv_err("Failed to set video encoder options");
        return -1;
    }

    /* Set codec context parameters */
    encoder_codec_context->height = params->enc_height != -1 ? params->enc_height : decoder_context->codec_context[index]->height;
    encoder_codec_context->width = params->enc_width != -1 ? params->enc_width : decoder_context->codec_context[index]->width;
    encoder_codec_context->time_base = decoder_context->codec_context[index]->time_base;
    encoder_codec_context->sample_aspect_ratio = decoder_context->codec_context[index]->sample_aspect_ratio;
    if (params->video_bitrate > 0)
        encoder_codec_context->bit_rate = params->video_bitrate;
    encoder_codec_context->rc_buffer_size = params->rc_buffer_size;
    if (params->rc_max_rate > 0)
        encoder_codec_context->rc_max_rate = params->rc_max_rate;

    // This needs to be set before open (ffmpeg samples have it wrong)
    if (encoder_context->format_context->oformat->flags & AVFMT_GLOBALHEADER) {
        encoder_codec_context->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    }

    /* Enable hls playlist format if output format is set to "hls" */
    if (!strcmp(params->format, "hls"))
        av_opt_set(encoder_context->format_context->priv_data, "hls_playlist", "1", 0);

#if 0
    /*
     * This part is disabled to prevent having YUV422 as output if we have some videos with pixel format YUV422.
     * YUV422 pixel format has problem in playing on mac.
     */
    int found_pix_fmt = 0;
    int i;
    /* Search for input pixel format in list of encoder pixel formats. */
    if ( encoder_context->codec[index]->pix_fmts ) {
        for (i=0; encoder_context->codec[index]->pix_fmts[i] >= 0; i++) {
            if (encoder_context->codec[index]->pix_fmts[i] == decoder_context->codec_context[index]->pix_fmt)
                found_pix_fmt = 1;
        }
    }

    /* If the codec is nvenc, or format is fmp4-segment set pixel format to AV_PIX_FMT_YUV420P. */
    if (!strcmp(params->format, "fmp4-segment") || !strcmp(params->ecodec, "h264_nvenc"))
        encoder_codec_context->pix_fmt = AV_PIX_FMT_YUV420P;
    else if (found_pix_fmt)
        /* If encoder supports the input pixel format then keep it */
        encoder_codec_context->pix_fmt = decoder_context->codec_context[index]->pix_fmt;
    else
        /* otherwise set encoder pixel format to AV_PIX_FMT_YUV420P. */
        encoder_codec_context->pix_fmt = AV_PIX_FMT_YUV420P;
#endif
    if (set_pixel_fmt(encoder_codec_context, params) < 0)
        return -1;

    if (!strcmp(params->ecodec, "h264_nvenc"))
        /* Set NVIDIA specific params if the encoder is NVIDIA */
        set_nvidia_params(encoder_context, decoder_context, params);
    else if (!strcmp(params->ecodec, "libx265"))
        /* Set H265 specific params (profile and level) */
        set_h265_params(encoder_context, decoder_context, params);
    else
        /* Set H264 specific params (profile and level) */
        set_h264_params(encoder_context, decoder_context, params);

    elv_log("Output pixel_format=%s, profile=%d, level=%d",
        av_get_pix_fmt_name(encoder_codec_context->pix_fmt),
        encoder_codec_context->profile,
        encoder_codec_context->level);

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
    int index = decoder_context->audio_stream_index[0];
    char *ecodec;
    AVFormatContext *format_context;
    int rc;

    if (index < 0) {
        elv_dbg("No audio stream detected by decoder.");
        return 0;
    }

    if (!decoder_context->codec_context[index]) {
        elv_err("Decoder codec context is NULL! index=%d", index);
        return -1;
    }

    /* If there are more than 1 audio stream do encode, we can't do bypass */
    if (params && params->bypass_transcoding && decoder_context->n_audio > 1) {
        elv_err("Can not bypass multiple audio streams, n_audio=%d", decoder_context->n_audio);
        return -1;
    }

    format_context = encoder_context->format_context2;
    ecodec = params->ecodec2;
    encoder_context->audio_last_dts = AV_NOPTS_VALUE;

    encoder_context->audio_stream_index[0] = index;
    encoder_context->n_audio = 1;

    encoder_context->stream[index] = avformat_new_stream(format_context, NULL);
    if (params->bypass_transcoding)
        encoder_context->codec[index] = avcodec_find_encoder(decoder_context->codec_context[index]->codec_id);
    else
        encoder_context->codec[index] = avcodec_find_encoder_by_name(ecodec);
    if (!encoder_context->codec[index]) {
        elv_err("Codec not found, codec_id=%s\n", avcodec_get_name(decoder_context->codec_context[index]->codec_id));
        return -1;
    }

    format_context->io_open = elv_io_open;
    format_context->io_close = elv_io_close;

    encoder_context->codec_context[index] = avcodec_alloc_context3(encoder_context->codec[index]);

    /* By default use decoder parameters */
    encoder_context->codec_context[index]->sample_rate = decoder_context->codec_context[index]->sample_rate;

    /* Set the default time_base based on input sample_rate */
    encoder_context->codec_context[index]->time_base = (AVRational){1, encoder_context->codec_context[index]->sample_rate};
    encoder_context->stream[index]->time_base = encoder_context->codec_context[index]->time_base;

    if (decoder_context->codec[index]->sample_fmts && params->bypass_transcoding)
        encoder_context->codec_context[index]->sample_fmt = decoder_context->codec[index]->sample_fmts[0];
    else if (encoder_context->codec[index]->sample_fmts && encoder_context->codec[index]->sample_fmts[0])
        encoder_context->codec_context[index]->sample_fmt = encoder_context->codec[index]->sample_fmts[0];
    else
        encoder_context->codec_context[index]->sample_fmt = AV_SAMPLE_FMT_FLTP;

    /* If the input stream is stereo the decoder_context->codec_context[index]->channel_layout is AV_CH_LAYOUT_STEREO */
    encoder_context->codec_context[index]->channel_layout = decoder_context->codec_context[index]->channel_layout;
    encoder_context->codec_context[index]->channels = av_get_channel_layout_nb_channels(encoder_context->codec_context[index]->channel_layout);

    /* If decoder channel layout is DOWNMIX, or params->ecodec == "aac" then set the channel layout to STEREO */
    if ( decoder_context->codec_context[index]->channel_layout == AV_CH_LAYOUT_STEREO_DOWNMIX || !strcmp(ecodec, "aac")) {
        /* This encoder is prepared specifically for AAC, therefore set the channel layout to AV_CH_LAYOUT_STEREO */
        encoder_context->codec_context[index]->channels = av_get_channel_layout_nb_channels(AV_CH_LAYOUT_STEREO);
        encoder_context->codec_context[index]->channel_layout = AV_CH_LAYOUT_STEREO;    // AV_CH_LAYOUT_STEREO is av_get_default_channel_layout(encoder_context->codec_context[index]->channels)
    }

    elv_dbg("ENCODER channels=%d, channel_layout=%d", encoder_context->codec_context[index]->channels, encoder_context->codec_context[index]->channel_layout);
    if (params->sample_rate > 0 && strcmp(ecodec, "aac")) {
        /*
         * Audio resampling, which is active for aac encoder, needs more work to adjust sampling properly
         * when input sample rate is different from output sample rate.
         * Therefore, set the sample rate to params->sample_rate if the encoder is not aac (-RM).
         */
        encoder_context->codec_context[index]->sample_rate = params->sample_rate;

        /* Update timebase for the new sample rate */
        encoder_context->codec_context[index]->time_base = (AVRational){1, params->sample_rate};
        encoder_context->stream[index]->time_base = (AVRational){1, params->sample_rate};
    }

    encoder_context->codec_context[index]->bit_rate = params->audio_bitrate;

    /* Allow the use of the experimental AAC encoder. */
    encoder_context->codec_context[index]->strict_std_compliance = FF_COMPLIANCE_EXPERIMENTAL;

    rc = set_encoder_options(encoder_context, decoder_context, params, encoder_context->audio_stream_index[0],
        encoder_context->stream[encoder_context->audio_stream_index[0]]->time_base.den);
    if (rc < 0) {
        elv_err("Failed to set audio encoder options");
        return -1;
    }

    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];
    /* Some container formats (like MP4) require global headers to be present.
     * Mark the encoder so that it behaves accordingly. */
    if (format_context->oformat->flags & AVFMT_GLOBALHEADER)
        encoder_codec_context->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

    /* Open audio encoder codec */
    if (avcodec_open2(encoder_context->codec_context[index], encoder_context->codec[index], NULL) < 0) {
        elv_dbg("Could not open encoder for audio, index=%d", index);
        return -1;
    }

    elv_dbg("encoder audio stream index=%d, bitrate=%d, sample_fmts=%d, timebase=%d, output frame_size=%d, sample_rate=%d",
        index, encoder_context->codec_context[index]->bit_rate, encoder_context->codec_context[index]->sample_fmt,
        encoder_context->codec_context[index]->time_base.den, encoder_context->codec_context[index]->frame_size,
        encoder_context->codec_context[index]->sample_rate);

    if (avcodec_parameters_from_context(
            encoder_context->stream[index]->codecpar,
            encoder_context->codec_context[index]) < 0) {
        elv_err("Failed to copy encoder parameters to output stream");
        return -1;

    }

#ifdef USE_RESAMPLE_AAC
    if (!strcmp(ecodec, "aac")) {
        init_resampler(decoder_context->codec_context[index], encoder_context->codec_context[index],
                       &decoder_context->resampler_context);

        /* Create the FIFO buffer based on the specified output sample format. */
        if (!(decoder_context->fifo = av_audio_fifo_alloc(encoder_context->codec_context[index]->sample_fmt,
                encoder_context->codec_context[index]->channels, 1))) {
            elv_err("Failed to allocate audio FIFO");
            return -1;
        }
    }
#endif

    encoder_context->audio_enc_stream_index = index;
    return 0;
}

static int
prepare_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    avpipe_io_handler_t *out_handlers,
    ioctx_t *inctx,
    txparams_t *params)
{
    out_tracker_t *out_tracker;
    char *filename = "";
    char *filename2 = "";
    char *format = params->format;

    /*
     * TODO: passing "hls" format needs some development in FF to produce stream index for audio/video.
     * I will keep hls as before to go to dashenc.c
     */
    if (!strcmp(params->format, "hls"))
        format = "dash";
    else if (!strcmp(params->format, "mp4")) {
        filename = "mp4-stream.mp4";
        if (params->tx_type == tx_all)
            filename2 = "mp4-astream.mp4";
    } else if (!strcmp(params->format, "fmp4")) {
        filename = "fmp4-stream.mp4";
        /* fmp4 is actually mp4 format with a fragmented flag */
        format = "mp4";
        if (params->tx_type == tx_all)
            filename2 = "fmp4-astream.mp4";
    } else if (!strcmp(params->format, "segment")) {
        filename = "segment-%05d.mp4";
        if (params->tx_type == tx_all)
            filename2 = "segment-audio-%05d.mp4";
    } else if (!strcmp(params->format, "fmp4-segment")) {
        /* Fragmented mp4 segment */
        format = "segment";
        if (params->tx_type & tx_video)
            filename = "fsegment-video-%05d.mp4";
        if (params->tx_type & tx_audio)
            filename2 = "fsegment-audio-%05d.mp4";
    }

    /*
     * Allocate an AVFormatContext for output.
     * Setting 3th paramter to "dash" determines the output file format and avoids guessing
     * output file format using filename in ffmpeg library.
     */
    if (params->tx_type & tx_video) {
        avformat_alloc_output_context2(&encoder_context->format_context, NULL, format, filename);
        if (!encoder_context->format_context) {
            elv_dbg("could not allocate memory for output format");
            return -1;
        }
    }
    if (params->tx_type & tx_audio) {
        avformat_alloc_output_context2(&encoder_context->format_context2, NULL, format, filename2);
        if (!encoder_context->format_context2) {
            elv_dbg("could not allocate memory for audio output format");
            return -1;
        }
    }

    // Encryption applies to both audio and video
    // PENDING (RM) Set the keys for audio if tx_type == tx_all
    switch (params->crypt_scheme) {
    case crypt_aes128:
        if (params->tx_type & tx_video) {
            av_opt_set(encoder_context->format_context->priv_data, "hls_enc", "1", 0);
            if (params->crypt_iv != NULL)
                av_opt_set(encoder_context->format_context->priv_data, "hls_enc_iv",
                           params->crypt_iv, 0);
            if (params->crypt_key != NULL)
                av_opt_set(encoder_context->format_context->priv_data,
                           "hls_enc_key", params->crypt_key, 0);
            if (params->crypt_key_url != NULL)
                av_opt_set(encoder_context->format_context->priv_data,
                           "hls_enc_key_url", params->crypt_key_url, 0);
        }
        if (params->tx_type & tx_audio) {
            av_opt_set(encoder_context->format_context2->priv_data, "hls_enc", "1", 0);
            if (params->crypt_iv != NULL)
                av_opt_set(encoder_context->format_context2->priv_data, "hls_enc_iv",
                           params->crypt_iv, 0);
            if (params->crypt_key != NULL)
                av_opt_set(encoder_context->format_context2->priv_data,
                           "hls_enc_key", params->crypt_key, 0);
            if (params->crypt_key_url != NULL)
                av_opt_set(encoder_context->format_context2->priv_data,
                           "hls_enc_key_url", params->crypt_key_url, 0);
        }
        break;
    case crypt_cenc:
        /* PENDING (RM) after debugging cbcs we can remove old encryption scheme names. */
        if (params->tx_type & tx_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                      "encryption_scheme", "cenc", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-ctr", 0);
        }
        if (params->tx_type & tx_audio) {
            av_opt_set(encoder_context->format_context2->priv_data,
                      "encryption_scheme", "cenc", 0);
            av_opt_set(encoder_context->format_context2->priv_data,
                       "encryption_scheme", "cenc-aes-ctr", 0);
        }
        break;
    case crypt_cbc1:
        if (params->tx_type & tx_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cbc1", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-cbc", 0);
        }
        if (params->tx_type & tx_audio) {
            av_opt_set(encoder_context->format_context2->priv_data,
                       "encryption_scheme", "cbc1", 0);
            av_opt_set(encoder_context->format_context2->priv_data,
                       "encryption_scheme", "cenc-aes-cbc", 0);
        }
        break;
    case crypt_cens:
        if (params->tx_type & tx_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cens", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-ctr-pattern", 0);
        }
        if (params->tx_type & tx_audio) {
            av_opt_set(encoder_context->format_context2->priv_data,
                       "encryption_scheme", "cens", 0);
            av_opt_set(encoder_context->format_context2->priv_data,
                       "encryption_scheme", "cenc-aes-ctr-pattern", 0);
        }
        break;
    case crypt_cbcs:
        if (params->tx_type & tx_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cbcs", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-cbc-pattern", 0);
            av_opt_set(encoder_context->format_context->priv_data, "encryption_iv",
                params->crypt_iv, 0);
            av_opt_set(encoder_context->format_context->priv_data, "hls_enc_iv",        /* To remove */
                params->crypt_iv, 0);
        }
        if (params->tx_type & tx_audio) {
            av_opt_set(encoder_context->format_context2->priv_data,
                       "encryption_scheme", "cbcs", 0);
            av_opt_set(encoder_context->format_context2->priv_data,
                       "encryption_scheme", "cenc-aes-cbc-pattern", 0);
            av_opt_set(encoder_context->format_context2->priv_data, "encryption_iv",
                params->crypt_iv, 0);
            av_opt_set(encoder_context->format_context2->priv_data, "hls_enc_iv",        /* To remove */
                params->crypt_iv, 0);
        }
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
        if (params->tx_type & tx_video) {
            av_opt_set(encoder_context->format_context->priv_data, "encryption_kid",
                       params->crypt_kid, 0);
            av_opt_set(encoder_context->format_context->priv_data, "encryption_key",
                       params->crypt_key, 0);
        }
        if (params->tx_type & tx_audio) {
            av_opt_set(encoder_context->format_context2->priv_data, "encryption_kid",
                       params->crypt_kid, 0);
            av_opt_set(encoder_context->format_context2->priv_data, "encryption_key",
                       params->crypt_key, 0);
        }
    default:
        break;
    }

    if (params->tx_type & tx_video) {
        if (prepare_video_encoder(encoder_context, decoder_context, params)) {
            elv_err("Failure in preparing video encoder");
            return -1;
        }
    }

    if (params->tx_type & tx_audio) {
        encoder_context->audio_enc_stream_index = -1;
        if (prepare_audio_encoder(encoder_context, decoder_context, params)) {
            elv_err("Failure in preparing audio copy");
            return -1;
        }
    }

    /*
     * Allocate an array of 2 out_handler_t: one for video and one for audio output stream.
     * TODO: needs to allocate up to number of streams when transcoding multiple streams at the same time (RM)
     */
    if (params->tx_type & tx_video) {
        out_tracker = (out_tracker_t *) calloc(2, sizeof(out_tracker_t));
        out_tracker[0].out_handlers = out_tracker[1].out_handlers = out_handlers;
        out_tracker[0].inctx = out_tracker[1].inctx = inctx;
        out_tracker[0].video_stream_index = out_tracker[1].video_stream_index = decoder_context->video_stream_index;
        out_tracker[0].audio_stream_index = out_tracker[1].audio_stream_index = decoder_context->audio_stream_index[0];
        out_tracker[0].seg_index = out_tracker[1].seg_index = atoi(params->start_segment_str);
        out_tracker[0].encoder_ctx = out_tracker[1].encoder_ctx = encoder_context;
        out_tracker[0].tx_type = out_tracker[1].tx_type = tx_video;
        encoder_context->format_context->avpipe_opaque = out_tracker;
    }

    if (params->tx_type & tx_audio) {
        out_tracker = (out_tracker_t *) calloc(2, sizeof(out_tracker_t));
        out_tracker[0].out_handlers = out_tracker[1].out_handlers = out_handlers;
        out_tracker[0].inctx = out_tracker[1].inctx = inctx;
        out_tracker[0].video_stream_index = out_tracker[1].video_stream_index = decoder_context->video_stream_index;
        out_tracker[0].audio_stream_index = out_tracker[1].audio_stream_index = decoder_context->audio_stream_index[0];
        out_tracker[0].seg_index = out_tracker[1].seg_index = atoi(params->start_segment_str);
        out_tracker[0].encoder_ctx = out_tracker[1].encoder_ctx = encoder_context;
        out_tracker[0].tx_type = out_tracker[1].tx_type = tx_audio;
        encoder_context->format_context2->avpipe_opaque = out_tracker;
    }

    dump_encoder(inctx->url, encoder_context->format_context, params);
    dump_codec_context(encoder_context->codec_context[encoder_context->video_stream_index]);
    dump_encoder(inctx->url, encoder_context->format_context2, params);
    dump_codec_context(encoder_context->codec_context[encoder_context->audio_stream_index[0]]);

    return 0;
}

static void
set_idr_frame_key_flag(
    AVFrame *frame,
    coderctx_t *encoder_context,
    txparams_t *params)
{
    if (!frame)
        return;

    /* No need to set IDR key frame if the tx_type is not tx_video. */
    if ((params->tx_type & tx_video) == 0)
        return;

    /*
     * If format is "dash" or "hls" then don't clear the flag, because dash/hls uses pict_type to determine end of segment.
     * The reset of the formats would be good to clear before encoding (see doc/examples/transcoding.c).
     */
    if (strcmp(params->format, "dash") && strcmp(params->format, "hls"))
        frame->pict_type = AV_PICTURE_TYPE_NONE;

    /*
     * Set key frame in the beginning of every segment (doesn't matter it is mez segment or abr segment).
     */
    if (!strcmp(params->format, "fmp4-segment") || !strcmp(params->format, "segment") ||
        !strcmp(params->format, "dash") || !strcmp(params->format, "hls")) {
        if (frame->pts >= encoder_context->last_key_frame + params->video_seg_duration_ts) {
            elv_dbg("FRAME SET KEY flag, seg_duration_ts=%d pts=%"PRId64, params->video_seg_duration_ts, frame->pts);
            frame->pict_type = AV_PICTURE_TYPE_I;
            encoder_context->last_key_frame = frame->pts;
        }
    }

    if (params->force_keyint > 0) {
        if (encoder_context->forced_keyint_countdown == 0) {
            elv_dbg("FRAME SET KEY flag, forced_keyint=%d pts=%"PRId64, params->force_keyint, frame->pts);
            frame->pict_type = AV_PICTURE_TYPE_I;
            encoder_context->forced_keyint_countdown = params->force_keyint;
        }
        encoder_context->forced_keyint_countdown --;
    }
}

static int
should_skip_encoding(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    int stream_index,
    txparams_t *p,
    AVFrame *frame)
{
    if (!frame ||
        p->tx_type == tx_audio_join ||
        p->tx_type == tx_audio_merge)
        return 0;

    /* For MPEG-TS - skip a fixed duration at the beginning to sync audio and vide
     * Audio starts encoding right away but video will drop frames up until the first i-frame.
     * Skipping an equal amount of both audio and video ensures they start encoding at approximately
     * the same time (generally less that one frame duration apart).
     */
    if (p->sync_audio_to_stream_id < 0) {
        const int64_t mpegts_skip_offset = 10; /* seoncds */
        if (mpegts_skip_offset > 0 && decoder_context->is_mpegts && (!strcmp(p->format, "fmp4-segment"))) {
            int64_t time_base = decoder_context->stream[stream_index]->time_base.den;
            int64_t pts_offset = encoder_context->first_read_frame_pts[stream_index];
            if (!strcmp(p->ecodec2, "aac")) {
                time_base = encoder_context->stream[stream_index]->time_base.den;
#ifdef USE_RESAMPLE_AAC
                pts_offset = 0; /* AAC always starts at PTS 0 */
#endif
            }
            if (frame->pts - pts_offset < mpegts_skip_offset * time_base) {
                elv_dbg("ENCODE skip frame early mpegts stream=%d pts=%" PRId64" first=%" PRId64 " time_base=%" PRId64,
                    stream_index, frame->pts, pts_offset, time_base);
                return 1;
            }
        }
    }

    int64_t frame_in_pts_offset;

    if (selected_decoded_audio(decoder_context, stream_index) >= 0)
        frame_in_pts_offset = frame->pts - decoder_context->audio_input_start_pts;
    else
        frame_in_pts_offset = frame->pts - decoder_context->video_input_start_pts;

    /* If there is no video transcoding return 0 */
    if ((p->tx_type & tx_video) == 0)
        return 0;

    /* Drop frames before the desired 'start_time'
     * If the format is dash or hls, we skip the frames in skip_until_start_time_pts()
     * without decoding the frame.
     */
    if (p->start_time_ts > 0 &&
        frame_in_pts_offset < p->start_time_ts &&
        strcmp(p->format, "dash") &&
        strcmp(p->format, "hls")) {
        elv_dbg("ENCODE skip frame early pts=%" PRId64 ", frame_in_pts_offset=%" PRId64 ", start_time_ts=%" PRId64,
            frame->pts, frame_in_pts_offset, p->start_time_ts);
        return 1;
    }

    /* Skip beginning based on input packet pts */
    if (p->skip_over_pts > 0 && frame->pts <= p->skip_over_pts) {
        elv_dbg("ENCODE skip frame early pts=%" PRId64 ", frame_in_pts_offset=%" PRId64 ", skip_over_pts=%" PRId64,
            frame->pts, frame_in_pts_offset, p->skip_over_pts);
        return 1;
    }

    /* To allow for packet reordering frames can come with pts past the desired duration */
    if (p->duration_ts > 0) {
        const int64_t max_valid_ts = p->start_time_ts + p->duration_ts;
        if (frame_in_pts_offset >= max_valid_ts) {
            elv_dbg("ENCODE skip frame late pts=%" PRId64 ", frame_in_pts_offset=%" PRId64 ", max_valid_ts=%" PRId64,
                frame->pts, frame_in_pts_offset, max_valid_ts);
            return 1;
        }
    }

    return 0;
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

    if (selected_decoded_audio(decoder_context, stream_index) >= 0)
        format_context = encoder_context->format_context2;

    int skip = should_skip_encoding(decoder_context, encoder_context, stream_index, params, frame);
    if (skip)
        return 0;

    AVPacket *output_packet = av_packet_alloc();
    if (!output_packet) {
        elv_dbg("could not allocate memory for output packet");
        return -1;
    }

    // Prepare packet before encoding - adjust PTS and IDR frame signaling
    if (frame) {

        const char *st = stream_type_str(encoder_context, stream_index);

        // Adjust PTS if input stream starts at an arbitrary value (MPEG-TS/RTMP)
        if ((decoder_context->is_mpegts || decoder_context->is_rtmp)
            && (!strcmp(params->format, "fmp4-segment"))) {
            if (stream_index == decoder_context->video_stream_index) {
                if (encoder_context->first_encoding_video_pts == -1) {
                    /* Remember the first video PTS to use as an offset later */
                    encoder_context->first_encoding_video_pts = frame->pts;
                    elv_log("PTS first_encoding_video_pts=%"PRId64" dec=%"PRId64" read=%"PRId64" stream=%d:%s",
                        encoder_context->first_encoding_video_pts,
                        decoder_context->first_decoding_video_pts,
                        encoder_context->first_read_frame_pts[stream_index], params->tx_type, st);
                }

                // Adjust video frame pts such that first frame sent to the encoder has PTS 0
                if (frame->pts != AV_NOPTS_VALUE)
                    frame->pts -= encoder_context->first_encoding_video_pts;
                if (frame->pkt_dts != AV_NOPTS_VALUE)
                    frame->pkt_dts -= encoder_context->first_encoding_video_pts;
                if (frame->best_effort_timestamp != AV_NOPTS_VALUE)
                    frame->best_effort_timestamp -= encoder_context->first_encoding_video_pts;
            }
#ifndef USE_RESAMPLE_AAC
            else if (selected_decoded_audio(decoder_context, stream_index) >= 0) {
                if (encoder_context->first_encoding_audio_pts == -1) {
                    /* Remember the first video PTS to use as an offset later */
                    encoder_context->first_encoding_audio_pts = frame->pts;
                    elv_log("PTS first_encoding_audio_pts=%"PRId64" dec=%"PRId64" read=%"PRId64" stream=%d:%s",
                        encoder_context->first_encoding_audio_pts,
                        decoder_context->first_decoding_audio_pts,
                        encoder_context->first_read_frame_pts[stream_index], params->tx_type, st);
                }

                // Adjust audio frame pts such that first frame sent to the encoder has PTS 0
                if (frame->pts != AV_NOPTS_VALUE)
                    frame->pts -= encoder_context->first_encoding_audio_pts;
                if (frame->pkt_dts != AV_NOPTS_VALUE)
                    frame->pkt_dts -= encoder_context->first_encoding_audio_pts;
                if (frame->best_effort_timestamp != AV_NOPTS_VALUE)
                    frame->best_effort_timestamp -= encoder_context->first_encoding_audio_pts;
            }
#endif
        }

        // Signal if we need IDR frames
        if (params->tx_type & tx_video &&
            stream_index == decoder_context->video_stream_index) {
            set_idr_frame_key_flag(frame, encoder_context, params);
        }

        dump_frame(selected_decoded_audio(decoder_context, stream_index) >= 0,
            "TOENC ", codec_context->frame_number, frame, debug_frame_level);
    }

    ret = avcodec_send_frame(codec_context, frame);
    if (ret < 0) {
        elv_err("Failed to send frame for encoding err=%d", ret);
    }

    if (frame) {
        if (params->tx_type & tx_audio && selected_decoded_audio(decoder_context, stream_index) >= 0)
            encoder_context->audio_last_pts_sent_encode = frame->pts;
        else if (params->tx_type & tx_video && stream_index == decoder_context->video_stream_index)
            encoder_context->video_last_pts_sent_encode = frame->pts;
    }

    while (ret >= 0) {
        /* The packet must be initialized before receiving */
        av_init_packet(output_packet);
        output_packet->data = NULL;
        output_packet->size = 0;

        ret = avcodec_receive_packet(codec_context, output_packet);

        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
            if (debug_frame_level)
                elv_dbg("encode_frame() EAGAIN in receiving packet");
            break;
        } else if (ret < 0) {
            elv_err("Failure while receiving a packet from the encoder: %s", av_err2str(ret));
            return -1;
        }

        /*
         * Set output_packet->stream_index to zero if output format only has one stream.
         * Preserve the stream index if output_packet->stream_index fits in output format.
         * Otherwise return error.
         */
        if (format_context->nb_streams == 1) {
            output_packet->stream_index = 0;
        } else if (output_packet->stream_index >= format_context->nb_streams) {
            elv_err("Output packet stream_index=%d is more than the number of streams=%d in output format",
                output_packet->stream_index, format_context->nb_streams);
            return -1;
        }

        /*
         * Set packet duration manually if not set by the encoder.
         * The logic is borrowed from dashenc.c dash_write_packet.
         * The main problem not having a packet duration is the calculation of the track length which
         * always ends up one packet short and then all rate and duration calculcations are slightly skewed.
         * See get_cluster_duration()
         */
        if (params->tx_type == tx_video)
            assert(output_packet->duration == 0); /* Only to notice if this ever gets set */
        if (selected_decoded_audio(decoder_context, stream_index) >= 0 && params->tx_type == tx_all) {
            if (!output_packet->duration && encoder_context->audio_last_dts != AV_NOPTS_VALUE)
                output_packet->duration = output_packet->dts - encoder_context->audio_last_dts;
            encoder_context->audio_last_dts = output_packet->dts;
            encoder_context->audio_last_pts_encoded = output_packet->pts;
        } else {
            if (!output_packet->duration && encoder_context->video_last_dts != AV_NOPTS_VALUE)
                output_packet->duration = output_packet->dts - encoder_context->video_last_dts;
            encoder_context->video_last_dts = output_packet->dts;
            encoder_context->video_last_pts_encoded = output_packet->pts;
        }

        if (selected_decoded_audio(decoder_context, stream_index) >= 0)
            encoder_context->audio_pts = output_packet->pts;
        else
            encoder_context->video_pts = output_packet->pts;

        output_packet->pts += params->start_pts;
        output_packet->dts += params->start_pts;

        if (encoder_context->video_encoder_prev_pts > 0 &&
            stream_index == decoder_context->video_stream_index &&
            encoder_context->calculated_frame_duration > 0 &&
            output_packet->pts != AV_NOPTS_VALUE &&
            output_packet->pts - encoder_context->video_encoder_prev_pts >
                3*encoder_context->calculated_frame_duration) {
            elv_log("GAP detected, packet->pts=%"PRId64", video_encoder_prev_pts=%"PRId64,
                output_packet->pts, encoder_context->video_encoder_prev_pts);
        }

        if (stream_index == decoder_context->video_stream_index &&
            output_packet->pts != AV_NOPTS_VALUE)
            encoder_context->video_encoder_prev_pts = output_packet->pts;

        /* Rescale using the stream time_base (not the codec context). */
#ifdef USE_RESAMPLE_AAC
        /* Don't rescale if using audio FIFO - PTS is already set in output time base. */
        if (stream_index == decoder_context->video_stream_index &&
#else
        if (
#endif
            (decoder_context->stream[stream_index]->time_base.den !=
            encoder_context->stream[stream_index]->time_base.den ||
            decoder_context->stream[stream_index]->time_base.num !=
            encoder_context->stream[stream_index]->time_base.num)) {
            av_packet_rescale_ts(output_packet,
                decoder_context->stream[stream_index]->time_base,
                encoder_context->stream[stream_index]->time_base
            );
        }

        dump_packet(selected_decoded_audio(decoder_context, stream_index) >= 0,
            "OUT ", output_packet, debug_frame_level);

        /* mux encoded frame */
        ret = av_interleaved_write_frame(format_context, output_packet);
        if (ret != 0) {
            elv_err("Error %d writing output packet index=%d into stream_index=%d: %s",
                ret, output_packet->stream_index, stream_index, av_err2str(ret));
        }
    }
    av_packet_unref(output_packet);
    av_packet_free(&output_packet);
    return 0;
}

static int
do_bypass(
    int is_audio,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    txparams_t *p,
    int debug_frame_level)
{
    av_packet_rescale_ts(packet,
        decoder_context->stream[packet->stream_index]->time_base,
        encoder_context->stream[packet->stream_index]->time_base);

    packet->pts += p->start_pts;
    packet->dts += p->start_pts;

    dump_packet(is_audio, "BYPASS ", packet, debug_frame_level);

    AVFormatContext *format_context;

    if (is_audio)
        format_context = encoder_context->format_context2;
    else
        format_context = encoder_context->format_context;

    if (av_interleaved_write_frame(format_context, packet) < 0) {
        elv_err("Failure in copying bypass packet tx_type=%d", p->tx_type);
        return -1;
    }

    return 0;
}

static int
transcode_audio(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    AVFrame *frame,
    AVFrame *filt_frame,
    int stream_index,
    txparams_t *p,
    int debug_frame_level)
{
    int ret;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int audio_enc_stream_index = encoder_context->audio_enc_stream_index;
    int response;


    if (debug_frame_level)
        elv_dbg("DECODE stream_index=%d send_packet pts=%"PRId64" dts=%"PRId64
            " duration=%d, input frame_size=%d, output frame_size=%d, audio_output_pts=%"PRId64,
            stream_index, packet->pts, packet->dts, packet->duration, codec_context->frame_size,
            encoder_context->codec_context[audio_enc_stream_index]->frame_size, decoder_context->audio_output_pts);

    if (p->bypass_transcoding) {
        return do_bypass(1, decoder_context, encoder_context, packet, p, debug_frame_level);
    }

    response = avcodec_send_packet(codec_context, packet);
    if (response < 0) {
        elv_err("Failure while sending an audio packet to the decoder: err=%d, %s", response, av_err2str(response));
        // Ignore the error and continue
        response = 0;
        return response;
    }

    while (response >= 0) {
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN) || response == AVERROR_EOF) {
            break;
        } else if (response < 0) {
            elv_err("Failure while receiving a frame from the decoder: %s", av_err2str(response));
            return response;
        }

        if (decoder_context->first_decoding_audio_pts < 0)
            decoder_context->first_decoding_audio_pts = frame->pts;

        dump_frame(1, "IN ", codec_context->frame_number, frame, debug_frame_level);

        decoder_context->audio_pts = packet->pts;

        /* push the decoded frame into the filtergraph */
        for (int i=0; i<decoder_context->n_audio; i++) {
            /* push the decoded frame into the filtergraph */
            if (stream_index == decoder_context->audio_stream_index[i]) {
                if (av_buffersrc_add_frame_flags(decoder_context->audio_buffersrc_ctx[i], frame, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
                    elv_err("Failure in feeding into audio filtergraph source %d", i);
                    break;
                }
            }
        }

        /* pull filtered frames from the filtergraph */
        while (1) {
            ret = av_buffersink_get_frame(decoder_context->audio_buffersink_ctx, filt_frame);
            if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
                //elv_dbg("av_buffersink_get_frame() ret=EAGAIN");
                break;
            }

            if (ret < 0) {
                elv_err("Failed to execute audio frame filter ret=%d", ret);
                return -1;
            }

            dump_frame(1, "FILT ", codec_context->frame_number, filt_frame, debug_frame_level);
            encode_frame(decoder_context, encoder_context, filt_frame, encoder_context->audio_enc_stream_index, p, debug_frame_level);
            av_frame_unref(filt_frame);
        }

        av_frame_unref(frame);
    }
    return 0;
}

#ifdef USE_RESAMPLE_AAC
static int
transcode_audio_aac(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    AVFrame *frame,
    AVFrame *filt_frame,
    int stream_index,
    txparams_t *p,
    int debug_frame_level)
{
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    AVCodecContext *output_codec_context = encoder_context->codec_context[stream_index];
    SwrContext *resampler_context = decoder_context->resampler_context;
    int response;

    if (debug_frame_level)
        elv_dbg("DECODE stream_index=%d send_packet pts=%"PRId64" dts=%"PRId64" duration=%d, input frame_size=%d, output frame_size=%d",
            stream_index, packet->pts, packet->dts, packet->duration, codec_context->frame_size, encoder_context->codec_context[stream_index]->frame_size);

    if (p->bypass_transcoding) {
        return do_bypass(1, decoder_context, encoder_context, packet, p, debug_frame_level);
    }

    response = avcodec_send_packet(codec_context, packet);
    if (response < 0) {
        elv_err("Failure while sending an audio packet to the decoder: err=%d, %s", response, av_err2str(response));
        // Ignore the error and continue
        response = 0;
        return response;
    }

    while (response >= 0) {
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN) || response == AVERROR_EOF) {
            break;
        } else if (response < 0) {
            elv_err("Failure while receiving a frame from the decoder: %s", av_err2str(response));
            return response;
        }

        if (decoder_context->first_decoding_audio_pts < 0)
            decoder_context->first_decoding_audio_pts = frame->pts;

        dump_frame(1, "IN ", codec_context->frame_number, frame, debug_frame_level);

        if (decoder_context->audio_input_prev_pts < 0)
            decoder_context->audio_input_prev_pts = frame->pts;

        decoder_context->audio_pts = packet->pts;
        /* Temporary storage for the converted input samples. */
        uint8_t **converted_input_samples = NULL;
        int input_frame_size = codec_context->frame_size > 0 ? codec_context->frame_size : frame->nb_samples;

        if (init_converted_samples(&converted_input_samples, output_codec_context, input_frame_size)) {
            elv_err("Failed to allocate audio samples");
            return AVERROR_EXIT;
        }

        if (convert_samples((const uint8_t**)frame->extended_data, converted_input_samples,
                            input_frame_size, resampler_context)) {
            elv_err("Failed to convert audio samples");
            return AVERROR_EXIT;
        }

        /* Store the new samples in the FIFO buffer. */
        if (av_audio_fifo_write(decoder_context->fifo, (void **)converted_input_samples,
                input_frame_size) < input_frame_size) {
            elv_err("Failed to write input frame to fifo frame_size=%d", input_frame_size);
            return AVERROR_EXIT;
        }

        if (converted_input_samples) {
            av_freep(&converted_input_samples[0]);
            free(converted_input_samples);
        }

        int output_frame_size = encoder_context->codec_context[stream_index]->frame_size;

        while (av_audio_fifo_size(decoder_context->fifo) >= output_frame_size) {

            /* PENDING(SSS) - reuse filt_frame instead of allocating each time here. Not freed */
            init_output_frame(&filt_frame, encoder_context->codec_context[stream_index], output_frame_size);

            /* Read as many samples from the FIFO buffer as required to fill the frame.
             * The samples are stored in the frame temporarily. */
            if (av_audio_fifo_read(decoder_context->fifo, (void **)filt_frame->data, output_frame_size)
                    < output_frame_size) {
                elv_err("Failed to read input samples from fifo frame_size=%d", output_frame_size);
                av_frame_unref(filt_frame);
                return AVERROR_EXIT;
            }

            int64_t d;
            // TODO this handles packet loss but not irregular PTS deltas that are not equal to pkt_duration
            // If audio frames have irregular PTS the code will produce incorrect results - disabled by default
            if (p->audio_fill_gap &&
                (decoder_context->is_mpegts && frame->pts - decoder_context->audio_input_prev_pts > frame->pkt_duration)) {
                /*
                 * float pkt_ratio = ((float)(encoder_context->codec_context[stream_index]->sample_rate * frame->pkt_duration)) /
                 *                    (((float) decoder_context->stream[stream_index]->time_base.den) * filt_frame->nb_samples);
                 * pkt_ratio shows the transcoding ratio of output frames (packets) to input frames (packets).
                 * For example, if input timebase is 90000 with pkt_duration = 2880,
                 * and output sample rate is 48000 with frame duration = 1024 then pkt_ratio = 3/2 that means
                 * for every 2 input audio frames, there would be 3 output audio frame.
                 * Now to calculate output packet pts from input packet pts:
                 * output_pkt_pts = decoder_context->audio_output_pts + d
                 * where d = ((float) (frame->pts - decoder_context->audio_input_prev_pts) / frame->pkt_duration) * pkt_ratio * filt_frame->nb_samples
                 * After simplification we will have d as follows:
                 */
                d = (frame->pts - decoder_context->audio_input_prev_pts) * (encoder_context->codec_context[stream_index]->time_base.den) /
                        decoder_context->stream[stream_index]->time_base.den;

                /* Round up d to nearest multiple of output frame size */
                d = ((d+output_frame_size-1)/output_frame_size)*output_frame_size;
                elv_dbg("AUDIO JUMP from=%"PRId64", to=%"PRId64", frame->pts=%"PRId64", audio_input_prev_pts=%"PRId64", pkt_duration=%d",
                    decoder_context->audio_output_pts,
                    decoder_context->audio_output_pts + d,
                    frame->pts,
                    decoder_context->audio_input_prev_pts,
                    frame->pkt_duration);
            } else {
                d = output_frame_size;
            }

            decoder_context->audio_input_prev_pts = frame->pts;

            while (d > 0) {
                /* When using FIFO frames no longer have PTS */
                filt_frame->pkt_dts = filt_frame->pts = decoder_context->audio_output_pts;

                if (decoder_context->audio_duration < filt_frame->pts) {
                    decoder_context->audio_duration = filt_frame->pts;

                    int should_skip = 0;
                    int64_t frame_in_pts_offset = frame->pts - decoder_context->audio_input_start_pts;
                    /* If frame PTS < start_time_ts then don't encode audio frame */
                    if (p->start_time_ts > 0 && frame_in_pts_offset < p->start_time_ts) {
                         elv_dbg("ENCODE skip audio frame early pts=%" PRId64
                            ", frame_in_pts_offset=%" PRId64 ", start_time_ts=%" PRId64,
                            filt_frame->pts, frame_in_pts_offset, p->start_time_ts);
                        should_skip = 1;
                    }

                    if (!should_skip)
                        encode_frame(decoder_context, encoder_context, filt_frame, stream_index, p, debug_frame_level);
                }
                else {
                    elv_log("SKIP audio frame pts=%"PRId64", duration=%"PRId64,
                        filt_frame->pts, decoder_context->audio_duration);
                }

                if (p->audio_fill_gap) {
                    decoder_context->audio_output_pts += output_frame_size;
                    d -= output_frame_size;
                } else {
                    decoder_context->audio_output_pts += d;
                    d = 0;
                }
            }

            av_frame_unref(filt_frame);
            av_frame_free(&filt_frame);
        }

        av_frame_unref(frame);
    }
    return 0;
}
#endif

static int
transcode_video(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    AVFrame *frame,
    AVFrame *filt_frame,
    int stream_index,
    txparams_t *p,
    int do_instrument,
    int debug_frame_level)
{
    int ret;
    struct timeval tv;
    u_int64_t since;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int response;

    if (debug_frame_level)
        elv_dbg("DECODE stream_index=%d send_packet pts=%"PRId64" dts=%"PRId64" duration=%d", stream_index, packet->pts, packet->dts, packet->duration);

    if (p->bypass_transcoding) {
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

        return do_bypass(0, decoder_context, encoder_context, packet, p, debug_frame_level);
    }

    /* send packet to decoder */
    response = avcodec_send_packet(codec_context, packet);
    if (response < 0) {
        elv_err("Failure while sending a video packet to the decoder: %s (%d)", av_err2str(response), response);
        if (response == AVERROR_INVALIDDATA)
            /*
             * AVERROR_INVALIDDATA means the frame is invalid (mostly because of bad header).
             * To avoid premature termination jump over the bad frame and continue decoding.
             */
            response = 0;
        return response;
    }

    while (response >= 0) {
        elv_get_time(&tv);

        /* read decoded frame from decoder */
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN) || response == AVERROR_EOF) {
            break;
        } else if (response < 0) {
            elv_err("Failure while receiving a frame from the decoder: %s", av_err2str(response));
            return response;
        }

        if (decoder_context->first_decoding_video_pts < 0)
            decoder_context->first_decoding_video_pts = frame->pts;

        /* If force_equal_fduration is set then frame_duration > 0 is true */
        if (decoder_context->frame_duration > 0) {
            elv_dbg("SET VIDEO PTS frame_num=%d, old_pts=%"PRId64", new_pts=%"PRId64", diff=%"PRId64", dts=%"PRId64,
                codec_context->frame_number,
                frame->pts,
                decoder_context->first_decoding_video_pts + decoder_context->frame_duration * (codec_context->frame_number - 1),
                decoder_context->first_decoding_video_pts + decoder_context->frame_duration * (codec_context->frame_number - 1) - frame->pts,
                frame->pkt_dts);
            /* Set the PTS and DTS of the frame to equalize frame durations */
            frame->pts = decoder_context->first_decoding_video_pts +
                decoder_context->frame_duration * (codec_context->frame_number - 1);
            frame->pkt_dts = frame->pts;
        }

        dump_frame(0, "IN ", codec_context->frame_number, frame, debug_frame_level);

        if (do_instrument) {
            elv_since(&tv, &since);
            elv_log("INSTRMNT avcodec_receive_frame time=%"PRId64, since);
        }

        decoder_context->video_pts = packet->pts;

        /* push the decoded frame into the filtergraph */
        elv_get_time(&tv);
        //if (av_buffersrc_add_frame_flags(decoder_context->buffersrc_ctx, frame, AV_BUFFERSRC_FLAG_KEEP_REF | AV_BUFFERSRC_FLAG_PUSH) < 0)
        if (av_buffersrc_add_frame_flags(decoder_context->video_buffersrc_ctx, frame, 0) < 0) {
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
            ret = av_buffersink_get_frame(decoder_context->video_buffersink_ctx, filt_frame);
            if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
                //elv_dbg("av_buffersink_get_frame() ret=EAGAIN");
                break;
            }

            if (ret < 0) {
                elv_err("Failed to execute frame filter ret=%d", ret);
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

            dump_frame(0, "FILT ", codec_context->frame_number, filt_frame, debug_frame_level);
            filt_frame->pkt_dts = filt_frame->pts;

            elv_get_time(&tv);
            if (decoder_context->video_duration < filt_frame->pts) {
                decoder_context->video_duration = filt_frame->pts;
                encode_frame(decoder_context, encoder_context, filt_frame, stream_index, p, debug_frame_level);
            } else {
                elv_log("SKIP video frame pts=%"PRId64", duration=%"PRId64,
                    filt_frame->pts, decoder_context->video_duration);
            }

            if (do_instrument) {
                elv_since(&tv, &since);
                elv_log("INSTRMNT encode_frame time=%"PRId64, since);
            }

            av_frame_unref(filt_frame);
        }
        av_frame_unref(frame);
    }
    return 0;
}

void *
transcode_video_func(
    void *p)
{
    txctx_t *xctx = (txctx_t *) p;
    coderctx_t *decoder_context = &xctx->decoder_ctx;
    coderctx_t *encoder_context = &xctx->encoder_ctx;
    txparams_t *params = xctx->params;
    xc_frame_t *xc_frame;
    int rc;

    AVFrame *frame = av_frame_alloc();
    AVFrame *filt_frame = av_frame_alloc();

    while (!xctx->stop || elv_channel_size(xctx->vc) > 0) {

        xc_frame = elv_channel_receive(xctx->vc);
        if (!xc_frame) {
            elv_dbg("trancode_video thread, there is no frame rc=%d", rc);
            continue;
        }

        AVPacket *packet = xc_frame->packet;
        if (!packet) {
            elv_err("transcode_video packet is NULL");
            continue;
        }

        dump_packet(0, "IN THREAD", packet, xctx->debug_frame_level);

        rc = transcode_video(
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
        av_packet_free(&packet);
        free(xc_frame);

        if (rc < 0) {
            elv_dbg("Stop video transcoding, rc=%d", rc);
            break;
        }
    }

    av_frame_free(&frame);
    av_frame_free(&filt_frame);

    elv_dbg("transcode_video_func stop=%d", xctx->stop);

    return NULL;
}

void *
transcode_audio_func(
    void *p)
{
    txctx_t *xctx = (txctx_t *) p;
    coderctx_t *decoder_context = &xctx->decoder_ctx;
    coderctx_t *encoder_context = &xctx->encoder_ctx;
    txparams_t *params = xctx->params;
    xc_frame_t *xc_frame;
    int rc;

    AVFrame *frame = av_frame_alloc();
    AVFrame *filt_frame = av_frame_alloc();

    while (!xctx->stop || elv_channel_size(xctx->ac) > 0) {

        xc_frame = elv_channel_receive(xctx->ac);
        if (!xc_frame) {
            elv_dbg("trancode_audio thread, there is no frame");
            continue;
        }

        AVPacket *packet = xc_frame->packet;
        if (!packet) {
            elv_err("transcode_video packet is NULL");
            continue;
        }

        dump_packet(1, "IN THREAD", packet, xctx->debug_frame_level);

#ifdef USE_RESAMPLE_AAC
        /*
         * If decoder frame_size is not set (or it is zero), then using fifo for transcoding would not work,
         * so fallback to use audio filtering for transcoding.
         * Optimal solution would be to make filtering working for both aac and other cases (RM).
         */
        if (!strcmp(params->ecodec2, "aac") &&
            params->tx_type != tx_audio_join &&
            params->tx_type != tx_audio_merge) {
            rc = transcode_audio_aac(
                decoder_context,
                encoder_context,
                packet,
                frame,
                filt_frame,
                packet->stream_index,
                params,
                xctx->debug_frame_level);
        } else {
            rc = transcode_audio(
                decoder_context,
                encoder_context,
                packet,
                frame,
                filt_frame,
                packet->stream_index,
                params,
                xctx->debug_frame_level);
        }
#else
        rc = transcode_audio(
            decoder_context,
            encoder_context,
            packet,
            frame,
            filt_frame,
            packet->stream_index,
            params,
            xctx->debug_frame_level);
#endif

        av_frame_unref(frame);
        av_frame_unref(filt_frame);
        av_packet_free(&packet);
        free(xc_frame);

        if (rc < 0) {
            elv_dbg("Stop audio transcoding, rc=%d", rc);
            break;
        }

    }

    av_frame_free(&frame);
    av_frame_free(&filt_frame);

    elv_dbg("transcode_audio_func stop=%d", xctx->stop);

    return NULL;
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
    int i;
    AVFrame *frame, *filt_frame;
    AVFilterContext *buffersink_ctx = decoder_context->video_buffersink_ctx;
    AVFilterContext *buffersrc_ctx = decoder_context->video_buffersrc_ctx;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int response = avcodec_send_packet(codec_context, NULL);	/* Passing NULL means flush the decoder buffers */

    frame = av_frame_alloc();
    filt_frame = av_frame_alloc();

    if (!p->bypass_transcoding &&
        (i = selected_decoded_audio(decoder_context, stream_index)) >= 0) {
        buffersrc_ctx = decoder_context->audio_buffersrc_ctx[i];
        buffersink_ctx = decoder_context->audio_buffersink_ctx;
    }

    while (response >=0) {
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN)) {
            break;
        }

        if (response == AVERROR_EOF) {
            elv_log("GOT EOF tx_type=%d, format=%s", p->tx_type, p->format);
            continue; // PENDING(SSS) why continue and not break?
        }

        dump_frame(selected_decoded_audio(decoder_context, stream_index) >= 0,
            "IN FLUSH", codec_context->frame_number, frame, debug_frame_level);

        if (codec_context->codec_type == AVMEDIA_TYPE_VIDEO ||
            codec_context->codec_type == AVMEDIA_TYPE_AUDIO) {

            /* push the decoded frame into the filtergraph */
            if (av_buffersrc_add_frame_flags(buffersrc_ctx, frame, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
                elv_err("Failure in feeding the filtergraph");
                break;
            }

            /* pull filtered frames from the filtergraph */
            while (1) {
                ret = av_buffersink_get_frame(buffersink_ctx, filt_frame);
                if (ret == AVERROR(EAGAIN)) {
                    break;
                }

                if (ret == AVERROR_EOF) {
                    elv_log("2 GOT EOF");
                    break;
                }

                dump_frame(selected_decoded_audio(decoder_context, stream_index) >= 0,
                    "FILT ", codec_context->frame_number, filt_frame, debug_frame_level);

                encode_frame(decoder_context, encoder_context, filt_frame, stream_index, p, debug_frame_level);
                av_frame_unref(filt_frame);
            }
        }
        av_frame_unref(frame);
    }

    av_frame_free(&filt_frame);
    av_frame_free(&frame);
    return 0;
}

int
should_stop_decoding(
    AVPacket *input_packet,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    txparams_t *params,
    int *frames_read,
    int *frames_read_past_duration,
    int frames_allowed_past_duration)
{
    int64_t input_packet_rel_pts = 0;

    if (decoder_context->cancelled)
        return 1;

    (*frames_read) ++;

    if (input_packet->stream_index != decoder_context->video_stream_index &&
        selected_decoded_audio(decoder_context, input_packet->stream_index) < 0)
        return 0;

    if (input_packet->stream_index == decoder_context->video_stream_index &&
        (params->tx_type & tx_video)) {
        if (decoder_context->video_input_start_pts == -1) {
            out_tracker_t *out_tracker = (out_tracker_t *) encoder_context->format_context->avpipe_opaque;
            avpipe_io_handler_t *out_handlers = out_tracker->out_handlers;
            ioctx_t *outctx = out_tracker->last_outctx;
            decoder_context->video_input_start_pts = input_packet->pts;
            elv_log("video_input_start_pts=%"PRId64, decoder_context->video_input_start_pts);
            if (outctx) {
                outctx->decoding_start_pts = input_packet->pts;
                out_handlers->avpipe_stater(outctx, out_stat_decoding_start_pts);
            }
        }

        input_packet_rel_pts = input_packet->pts - decoder_context->video_input_start_pts;
    } else if (selected_decoded_audio(decoder_context, input_packet->stream_index) >= 0 &&
        params->tx_type & tx_audio) {
        if (decoder_context->audio_input_start_pts == -1) {
            out_tracker_t *out_tracker = (out_tracker_t *) encoder_context->format_context2->avpipe_opaque;
            avpipe_io_handler_t *out_handlers = out_tracker->out_handlers;
            ioctx_t *outctx = out_tracker->last_outctx;
            decoder_context->audio_input_start_pts = input_packet->pts;
            elv_log("audio_input_start_pts=%"PRId64, decoder_context->audio_input_start_pts);
            if (outctx) {
                outctx->decoding_start_pts = input_packet->pts;
                out_handlers->avpipe_stater(outctx, out_stat_decoding_start_pts);
            }
        }

        input_packet_rel_pts = input_packet->pts - decoder_context->audio_input_start_pts;
    }

    if (params->duration_ts != -1 &&
        input_packet->pts != AV_NOPTS_VALUE &&
        input_packet_rel_pts >= params->start_time_ts + params->duration_ts) {

        (*frames_read_past_duration) ++;
        elv_dbg("DURATION OVER param start_time=%"PRId64" duration=%"PRId64" pkt pts=%"PRId64" rel_pts=%"PRId64" "
                "frames_read=%d past_duration=%d",
                params->start_time_ts, params->duration_ts, input_packet->pts, input_packet_rel_pts,
                *frames_read, *frames_read_past_duration);

        /* If it is a bypass simply return since there is no decoding/encoding involved */
        if (params->bypass_transcoding)
            return 1;

        /* Allow decoding past specified duration to accommodate reordered packets */
        if (*frames_read_past_duration > frames_allowed_past_duration) {
            elv_dbg("frames_read_past_duration=%d, frames_allowed_past_duration=%d",
                        *frames_read_past_duration, frames_allowed_past_duration);
            return 1;
        }
    }

    if (input_packet->pts != AV_NOPTS_VALUE) {
        if (selected_decoded_audio(decoder_context, input_packet->stream_index) >= 0 &&
            params->tx_type & tx_audio)
            encoder_context->audio_last_pts_read = input_packet->pts;
        else if (input_packet->stream_index == decoder_context->video_stream_index &&
            params->tx_type & tx_video)
            encoder_context->video_last_pts_read = input_packet->pts;
    }
    return 0;
}

static int
skip_until_start_time_pts(
    coderctx_t *decoder_context,
    AVPacket *input_packet,
    txparams_t *params)
{
    if (params->start_time_ts <= 0)
        return 0;

    /* If the format is not dash/hls then return.
     * For dash/hls format, we know the mezzanines are generated by avpipe
     * and there is no BFrames in mezzanines. Therefore, it is safe to skip
     * the frame without decoding the frame.
     */
    if (strcmp(params->format, "dash") && strcmp(params->format, "hls"))
        return 0;

    int64_t input_start_pts;
    if (params->tx_type == tx_video)
        input_start_pts = decoder_context->video_input_start_pts;
    else
        input_start_pts = decoder_context->audio_input_start_pts;

    const int64_t packet_in_pts_offset = input_packet->pts - input_start_pts;
    /* Drop frames before the desired 'start_time' */
    if (packet_in_pts_offset < params->start_time_ts) {
        elv_dbg("PREDECODE skip frame early pts=%" PRId64 ", start_time_ts=%" PRId64,
            input_packet->pts, params->start_time_ts);
        return 1;
    }

    return 0;
}

static int
skip_for_sync(
    coderctx_t *decoder_context,
    AVPacket *input_packet,
    txparams_t *params)
{
    if (params->sync_audio_to_stream_id < 0)
        return 0;

    /* No need to sync if:
     * - it is not mpeg ts
     * - or it is already synced
     * - or format is not fmp4-segment.
     */
    if (!decoder_context->is_mpegts ||
        decoder_context->mpegts_synced ||
        strcmp(params->format, "fmp4-segment"))
        return 0;

    /* If tx_video just skip until the first key frame */
    if (params->tx_type & tx_video) {
        if (input_packet->flags == AV_PKT_FLAG_KEY) {
            decoder_context->mpegts_synced = 1;
            return 0;
        }
        return 1;
    }

    /* We are tx_audio but processing both video and audio input streams */
    if (input_packet->stream_index == decoder_context->video_stream_index) {
        /* first_key_frame_pts points to first video key frame. */
        if (decoder_context->first_key_frame_pts < 0 &&
            input_packet->flags == AV_PKT_FLAG_KEY) {
            decoder_context->first_key_frame_pts = input_packet->pts;
            elv_log("PTS first_key_frame_pts=%"PRId64" sidx=%d",
                decoder_context->first_key_frame_pts, input_packet->stream_index);

            dump_packet(0, "SYNC ", input_packet, 1);
        }
        return 1;
    }

    /* We are tx_audio and processing the audio stream.
     * Skip until the audio PTS has reached the first video key frame PTS
     * PENDING(SSS) - this is incorrect if audio PTS is muxed ahead of video
     */
    if (decoder_context->first_key_frame_pts < 0 ||
        input_packet->pts < decoder_context->first_key_frame_pts) {
        elv_log("PTS sync skip audio_pts=%"PRId64" first_key_frame_pts=%"PRId64,
            input_packet->pts, decoder_context->first_key_frame_pts);
        dump_packet(1, "SYNC SKIP ", input_packet, 1);
        return 1;
    }

    decoder_context->mpegts_synced = 1;
    elv_log("PTS first_audio_frame_pts=%"PRId64, input_packet->pts);
    return 0;
}


/**
 * @brief   formats a base64 encoded data url.
 *
 * @param   filt_buf            buffer to receive the base64 encoded data url, caller has to free the buffer
 * @param   filt_buf_size       size of output buffer
 * @param   img_buf             input buffer containing binary image data
 * @param   img_buf_size        size of input buffer
 * @param   img_type            value from enum image_type
 *
 * @return  Returns length of filt_buf if transcoding is successful, otherwise -1.
 */
static int
get_overlay_filter_string(
    char **filt_buf,
    char *img_buf,
    int img_buf_size,
    int img_type)
{
    const char* data_template = "data\\:%s;base64,%s";
    char* encoded_data = NULL;
    int encoded_data_length = base64encode_len(img_buf_size);
    int ret = 0;
    int n = 0;
    int filt_buf_size;

    encoded_data = malloc(encoded_data_length + 1);
    base64encode(encoded_data, img_buf, img_buf_size);

    ret = filt_buf_size = encoded_data_length + 128;
    *filt_buf = (char *) calloc(filt_buf_size, 1);
    switch(img_type){
        case png_image:
        {
            n = snprintf(*filt_buf, filt_buf_size, data_template, "image/png", encoded_data);
            if (n < 0 || n >= filt_buf_size){
                elv_err("snprintf overflow for png, need %u", n);
                ret = -1;
            }
            goto cleanup;
        }
        case jpg_image:
        {
            n = snprintf(*filt_buf, filt_buf_size, data_template, "image/jpg", encoded_data);
            if (n < 0 || n >= filt_buf_size){
                elv_err("snprintf overflow for jpg, need %u", n);
                ret = -1;
            }
            goto cleanup;
        }
        case gif_image:
        {
            snprintf(*filt_buf, filt_buf_size, data_template, "image/gif", encoded_data);
            if (n < 0 || n >= filt_buf_size){
                elv_err("snprintf overflow for gif, need %u", n);
                ret = -1;
            }
            goto cleanup;
        }
        default:
            elv_log("get_overlay_filter_string passed invalid type %d", img_type);
            ret = -1;
            goto cleanup;
    }
cleanup:
    free(encoded_data);
    return ret;
}

static int
get_filter_str(
    char **filter_str,
    coderctx_t *encoder_context,
    txparams_t *params)
{
    *filter_str = NULL;

    if ((params->watermark_text && *params->watermark_text != '\0') || (params->watermark_timecode && *params->watermark_timecode != '\0')) {
        char local_filter_str[FILTER_STRING_SZ];
        int shadow_x = 0;
        int shadow_y = 0;
        int font_size = 0;
        const char* filterTemplate = "scale=%d:%d, drawtext=text='%s':fontcolor=%s:fontsize=%d:x=%s:y=%s:shadowx=%d:shadowy=%d:shadowcolor=%s:alpha=0.65";
        int ret = 0;

        /* Return an error if one of the watermark params is not set properly */
        if ((!params->watermark_font_color || *params->watermark_font_color == '\0') ||
            (!params->watermark_xloc || *params->watermark_xloc == '\0') ||
            (params->watermark_relative_sz > 1 || params->watermark_relative_sz < 0) ||
            (!params->watermark_yloc || *params->watermark_yloc == '\0') ||
            (params->watermark_shadow && (!params->watermark_shadow_color || *params->watermark_shadow_color == '\0'))) {
            elv_err("Watermark params are not set correctly. color=\"%s\", relative_size=\"%f\", xloc=\"%s\", yloc=\"%s\", shadow=%d, shadow_color=\"%s\"",
                params->watermark_font_color != NULL ? params->watermark_font_color : "",
                params->watermark_relative_sz,
                params->watermark_xloc != NULL ? params->watermark_xloc : "",
                params->watermark_yloc != NULL ? params->watermark_yloc : "",
                params->watermark_shadow,
                params->watermark_shadow_color != NULL ? params->watermark_shadow_color : "");
            return -1;
        }

        font_size = (int) (params->watermark_relative_sz * encoder_context->codec_context[encoder_context->video_stream_index]->height);
        if (params->watermark_shadow) {
            /* Calculate shadow x and y */
            shadow_x = shadow_y = font_size*DRAW_TEXT_SHADOW_OFFSET;
        }

        /* If timecode params are set then apply them, otherwise apply text watermark params */
        if (params->watermark_timecode && *params->watermark_timecode != '\0') {
            filterTemplate = "scale=%d:%d, drawtext=timecode='%s':rate=%f:fontcolor=%s:fontsize=%d:x=%s:y=%s:shadowx=%d:shadowy=%d:shadowcolor=%s:alpha=0.65";

            if (params->watermark_timecode_rate <= 0) {
                elv_err("Watermark timecode params are not set correctly. rate=%f", params->watermark_timecode_rate);
                return -1;
            }

            ret = snprintf(local_filter_str, FILTER_STRING_SZ, filterTemplate,
                encoder_context->codec_context[encoder_context->video_stream_index]->width,
                encoder_context->codec_context[encoder_context->video_stream_index]->height,
                params->watermark_timecode, params->watermark_timecode_rate, params->watermark_font_color, font_size,
                params->watermark_xloc, params->watermark_yloc,
                shadow_x, shadow_y, params->watermark_shadow_color);
        } else {
            ret = snprintf(local_filter_str, FILTER_STRING_SZ, filterTemplate,
                encoder_context->codec_context[encoder_context->video_stream_index]->width,
                encoder_context->codec_context[encoder_context->video_stream_index]->height,
                params->watermark_text, params->watermark_font_color, font_size,
                params->watermark_xloc, params->watermark_yloc,
                shadow_x, shadow_y, params->watermark_shadow_color);
        }

        elv_dbg("filterstr=%s, len=%d, x=%s, y=%s, relative-size=%f, ret=%d",
            local_filter_str, strlen(local_filter_str), params->watermark_xloc, params->watermark_yloc, params->watermark_relative_sz, ret);
        if (ret < 0) {
            return -1;
        } else if (ret >= FILTER_STRING_SZ) {
            elv_dbg("Not enough memory for watermark filter");
            return -1;
        }
        *filter_str = (char *) calloc(strlen(local_filter_str)+1, 1);
        strcpy(*filter_str, local_filter_str);
    } else if (params->watermark_overlay && params->watermark_overlay[0] != '\0') {
        char *filt_buf = NULL;
        int filt_buf_size;
        int filt_str_len;
        const char* filt_template = "[in] scale=%d:%d [in-1]; movie='%s', setpts=PTS-STARTPTS [over]; [in-1] setpts=PTS-STARTPTS [in-1a]; [in-1a][over]  overlay='%s:%s:alpha=0.1' [out]";

        /* Return an error if one of the watermark params is not set properly */
        if ((!params->watermark_xloc || *params->watermark_xloc == '\0') ||
            (params->watermark_overlay_type == unknown_image) ||
            (!params->watermark_yloc || *params->watermark_yloc == '\0')) {
            elv_err("Watermark overlay params are not set correctly. overlay_type=\"%d\", xloc=\"%s\", yloc=\"%s\"",
                params->watermark_overlay_type,
                params->watermark_xloc != NULL ? params->watermark_xloc : "",
                params->watermark_yloc != NULL ? params->watermark_yloc : "");
            return -1;
        }

        filt_buf_size = get_overlay_filter_string(&filt_buf, params->watermark_overlay, params->watermark_overlay_len, params->watermark_overlay_type);
        if (filt_buf_size < 0)
            return -1;

        filt_str_len = filt_buf_size+FILTER_STRING_SZ;
        *filter_str = (char *) calloc(filt_str_len, 1);
        int ret = snprintf(*filter_str, filt_str_len, filt_template,
                        encoder_context->codec_context[encoder_context->video_stream_index]->width,
                        encoder_context->codec_context[encoder_context->video_stream_index]->height,
                        filt_buf,
                        params->watermark_xloc, params->watermark_yloc);
        free(filt_buf);
        if (ret < 0) {
            free(*filter_str);
            return -1;
        } else if (ret >= filt_str_len) {
            free(*filter_str);
            elv_dbg("Not enough memory for overlay watermark filter");
            return -1;
        }
    } else {
        if (!encoder_context->codec_context[encoder_context->video_stream_index]) {
            elv_err("Failed to make filter string, invalid codec context (check params)");
            return -1;
        }
        *filter_str = (char *) calloc(FILTER_STRING_SZ, 1);
        sprintf(*filter_str, "scale=%d:%d",
            encoder_context->codec_context[encoder_context->video_stream_index]->width,
            encoder_context->codec_context[encoder_context->video_stream_index]->height);
            elv_dbg("FILTER scale=%s", *filter_str);
    }

    return 0;
}


int
avpipe_xc(
    txctx_t *txctx,
    int do_instrument,
    int debug_frame_level)
{
    /* Set scale filter */
    char *filter_str = NULL;
    coderctx_t *decoder_context = &txctx->decoder_ctx;
    coderctx_t *encoder_context = &txctx->encoder_ctx;
    txparams_t *params = txctx->params;
    int rc = 0;
    AVPacket *input_packet = NULL;
    AVFrame *input_frame = NULL;
    AVFrame *filt_frame = NULL;

    if (!params->bypass_transcoding &&
        (params->tx_type & tx_video)) {
        if (get_filter_str(&filter_str, encoder_context, params) < 0) {
            rc = -1;
            goto xc_done;
        }

        if (init_filters(filter_str, decoder_context, encoder_context, txctx->params) < 0) {
            free(filter_str);
            elv_err("Failed to initialize video filter");
            rc = -1;
            goto xc_done;
        }
        free(filter_str);
    }

    if (!params->bypass_transcoding &&
        (params->tx_type & tx_audio) &&
        params->tx_type != tx_audio_join &&
        params->tx_type != tx_audio_pan &&
        params->tx_type != tx_audio_merge &&
        init_audio_filters(decoder_context, encoder_context, txctx->params) < 0) {
        elv_err("Failed to initialize audio filter");
        rc = -1;
        goto xc_done;
    }

    if (!params->bypass_transcoding &&
        params->tx_type == tx_audio_pan &&
        init_audio_pan_filters(txctx->params->filter_descriptor, decoder_context, encoder_context) < 0) {
        elv_err("Failed to initialize audio pan filter");
        rc = -1;
        goto xc_done;
    }
        
    if (!params->bypass_transcoding &&
        params->tx_type == tx_audio_join &&
        init_audio_join_filters(decoder_context, encoder_context, txctx->params) < 0) {
        elv_err("Failed to initialize audio join filter");
        rc = -1;
        goto xc_done;
    }

    if ((params->tx_type & tx_video) &&
        avformat_write_header(encoder_context->format_context, NULL) < 0) {
        elv_err("Failed to open video output file");
        rc = -1;
        goto xc_done;
    }

    if (params->tx_type & tx_audio &&
        avformat_write_header(encoder_context->format_context2, NULL) < 0) {
        elv_err("Failed to open audio output file");
        rc = -1;
        goto xc_done;
    }

    if (params->tx_type & tx_video &&
        encoder_context->codec_context[encoder_context->video_stream_index] &&
        calc_timebase(encoder_context->codec_context[encoder_context->video_stream_index]->time_base.den)
            != encoder_context->stream[encoder_context->video_stream_index]->time_base.den) {
        elv_err("Internal error in calculating timebase, calc_timebase=%d, stream_timebase=%d",
            calc_timebase(encoder_context->codec_context[encoder_context->video_stream_index]->time_base.den),
            encoder_context->stream[encoder_context->video_stream_index]->time_base.den);
        rc = -1;
        goto xc_done;
    }

    /*
     * Sometimes avformat_write_header() changes the timebase of stream (for example 24 -> 512).
     * In this case, reset the segment duration ts with new value to become sure cutting the segments are done properly.
     * PENDING (RM): it might be needed to do the same thing for audio.
     */
#if 0
    /* Will be removed after some more testing */
    if (params->tx_type & tx_video &&
        encoder_context->codec_context[encoder_context->video_stream_index] &&
        encoder_context->codec_context[encoder_context->video_stream_index]->time_base.den
            != encoder_context->stream[encoder_context->video_stream_index]->time_base.den) {
        int64_t seg_duration_ts;
        if (params->seg_duration && strlen(params->seg_duration) > 0) {
            float seg_duration = atof(params->seg_duration);
            seg_duration_ts = seg_duration * encoder_context->stream[encoder_context->video_stream_index]->time_base.den;
        } else {
            seg_duration_ts = params->video_seg_duration_ts *
                encoder_context->stream[encoder_context->video_stream_index]->time_base.den /
                encoder_context->codec_context[encoder_context->video_stream_index]->time_base.den;
        }
        elv_log("Stream orig ts=%"PRId64", new ts=%"PRId64", resetting \"fmp4-segment\" segment_time to %s, seg_duration_ts=%"PRId64,
            encoder_context->codec_context[encoder_context->video_stream_index]->time_base.den,
            encoder_context->stream[encoder_context->video_stream_index]->time_base.den,
            params->seg_duration, seg_duration_ts);
        encoder_context->codec_context[encoder_context->video_stream_index]->time_base =
            encoder_context->stream[encoder_context->video_stream_index]->time_base;
        av_opt_set_int(encoder_context->format_context->priv_data, "segment_duration_ts", seg_duration_ts, 0);
    }
#endif

    if (params->tx_type & tx_video) {
        if (encoder_context->format_context->streams[0]->avg_frame_rate.num != 0 &&
            decoder_context->stream[0]->time_base.num != 0) {
            encoder_context->calculated_frame_duration =
                decoder_context->stream[0]->time_base.den * encoder_context->format_context->streams[0]->avg_frame_rate.den /
                    (encoder_context->format_context->streams[0]->avg_frame_rate.num * decoder_context->stream[0]->time_base.num);
        }
        elv_log("calculated_frame_duration=%d", encoder_context->calculated_frame_duration);
    }


    input_frame = av_frame_alloc();
    filt_frame = av_frame_alloc();

    if (!input_frame || !filt_frame) {
        elv_err("Failed to allocated memory for AVFrame");
        rc = -1;
        goto xc_done;
    }

    txctx->do_instrument = do_instrument;
    txctx->debug_frame_level = debug_frame_level;

    elv_dbg("START TIME %d SKIP_PTS %d, START PTS %d (output), DURATION %d",
        params->start_time_ts, params->skip_over_pts,
        params->start_pts, params->duration_ts);

#if INPUT_IS_SEEKABLE
    /* Seek to start position */
    if (params->start_time_ts > 0) {
        if (av_seek_frame(decoder_context->format_context,
                decoder_context->video_stream_index, params->start_time_ts, SEEK_SET) < 0) {
            elv_err("Failed seeking to desired start frame");
            rc = -1;
            goto xc_done;
        }
    }
#endif

    if (params->start_time_ts != -1) {
        if (params->tx_type == tx_video)
            encoder_context->format_context->start_time = params->start_time_ts;
        if (params->tx_type & tx_audio)
            encoder_context->format_context2->start_time = params->start_time_ts;
        /* PENDING (RM) add new start_time_ts for audio */
    }
    if (params->duration_ts != -1) {
        if (params->tx_type & tx_video)
            encoder_context->format_context->duration = params->duration_ts;
        if (params->tx_type & tx_audio)
            encoder_context->format_context2->duration = params->duration_ts;
    }

    decoder_context->video_input_start_pts = -1;
    decoder_context->audio_input_start_pts = -1;
    decoder_context->video_duration = -1;
    encoder_context->audio_duration = -1;
    decoder_context->audio_input_prev_pts = -1;
    encoder_context->video_encoder_prev_pts = -1;
    decoder_context->first_decoding_video_pts = -1;
    decoder_context->first_decoding_audio_pts = -1;
    encoder_context->first_encoding_video_pts = -1;
    encoder_context->first_encoding_audio_pts = -1;
    for (int j=0; j<MAX_STREAMS; j++)
        encoder_context->first_read_frame_pts[j] = -1;
    decoder_context->first_key_frame_pts = -1;
    decoder_context->mpegts_synced = 0;

    int64_t video_last_dts = 0;
    int frames_read = 0;
    int frames_read_past_duration = 0;
    const int frames_allowed_past_duration = 5;

    while (1) {
        input_packet = av_packet_alloc();
        if (!input_packet) {
            elv_err("Failed to allocated memory for AVPacket");
            return -1;
        }

        rc = av_read_frame(decoder_context->format_context, input_packet);
        if (rc < 0) {
            av_packet_free(&input_packet);
            break;
        }

        const char *st = stream_type_str(encoder_context, input_packet->stream_index);
        int stream_index = input_packet->stream_index;

        // Record PTS of first frame read - excute only for the desired stream
        if ((stream_index == decoder_context->video_stream_index && (params->tx_type & tx_video)) ||
            (selected_decoded_audio(decoder_context, stream_index) >= 0 && (params->tx_type & tx_audio))) {

            if (encoder_context->first_read_frame_pts[stream_index] == -1 && input_packet->pts != AV_NOPTS_VALUE) {
                encoder_context->first_read_frame_pts[stream_index] = input_packet->pts;
                elv_log("PTS first_read_frame=%"PRId64" stream=%d:%d:%s sidx=%d",
                    encoder_context->first_read_frame_pts[stream_index], params->tx_type, params->stream_id,
                    st, stream_index);
            } else if (input_packet->pts != AV_NOPTS_VALUE && input_packet->pts < encoder_context->first_read_frame_pts[stream_index]) {
                /* Due to b-frame reordering */
                encoder_context->first_read_frame_pts[stream_index] = input_packet->pts;
                elv_log("PTS first_read_frame reorder new=%"PRId64" stream=%d:%d:%s",
                    encoder_context->first_read_frame_pts, params->tx_type, params->stream_id, st);
            }
        }

        /* Execute for both audio and video streams:
         * - used for syncing audio to first video key frame
         */
        if (input_packet->stream_index == decoder_context->video_stream_index ||
            selected_decoded_audio(decoder_context, input_packet->stream_index) >= 0) {
            if (skip_for_sync(decoder_context, input_packet, params)) {
                av_packet_unref(input_packet);
                av_packet_free(&input_packet);
                continue;
            }
        }

        // Excute only for the desired stream
        if ((input_packet->stream_index == decoder_context->video_stream_index && (params->tx_type & tx_video)) ||
            (selected_decoded_audio(decoder_context, input_packet->stream_index) >= 0 && (params->tx_type & tx_audio))) {
            // Stop when we reached the desired duration (duration -1 means 'entire input stream')
            // PENDING(SSS) - this logic can be done after decoding where we know concretely that we decoded all frames
            // we need to encode.
            if (should_stop_decoding(input_packet, decoder_context, encoder_context,
                params, &frames_read, &frames_read_past_duration, frames_allowed_past_duration))
                break;

            /*
             * Skip the input packet if the packet timestamp is smaller than start_time_ts.
             * The fact that avpipe mezzanine generated files don't have B-frames let us to skip before decoding.
             * The assumption here is that the input stream does not have any B-frames, otherwise we can not skip
             * the input packets without decoding.
             * Having this logic (here) before decoding increases the performance and saves CPU.
             * PENDING(RM) - add a validation to check input stream doesn't have any B-frames.
             */
            if (input_packet &&
                params->start_time_ts > 0 &&
                (params->tx_type == tx_video || params->tx_type == tx_audio) &&
                skip_until_start_time_pts(decoder_context, input_packet, params)) {
                    av_packet_unref(input_packet);
                    av_packet_free(&input_packet);
                    continue;
            }
        }

        if (input_packet->stream_index == decoder_context->video_stream_index &&
            (params->tx_type & tx_video)) {
            // Video packet
            dump_packet(0, "IN ", input_packet, debug_frame_level);

            // Assert DTS is growing as expected (accommodate non integer and irregular frame duration)
            if (video_last_dts + input_packet->duration * 1.5 < input_packet->dts &&
                video_last_dts != 0 && input_packet->duration > 0) {
                elv_log("Expected dts == last_dts + duration - video_last_dts=%" PRId64 " duration=%" PRId64 " dts=%" PRId64,
                    video_last_dts, input_packet->duration, input_packet->dts);
            }
            video_last_dts = input_packet->dts;

            xc_frame_t *xc_frame = (xc_frame_t *) calloc(1, sizeof(xc_frame_t));
            xc_frame->packet = input_packet;
            xc_frame->stream_index = input_packet->stream_index;
            elv_channel_send(txctx->vc, xc_frame);

        } else if (selected_decoded_audio(decoder_context, input_packet->stream_index) >= 0 &&
            params->tx_type & tx_audio) {

            encoder_context->audio_last_dts = input_packet->dts;

            dump_packet(1, "IN ", input_packet, debug_frame_level);

            xc_frame_t *xc_frame = (xc_frame_t *) calloc(1, sizeof(xc_frame_t));
            xc_frame->packet = input_packet;
            xc_frame->stream_index = input_packet->stream_index;
            elv_channel_send(txctx->ac, xc_frame);

        } else if (debug_frame_level) {
            elv_dbg("Skip stream - packet index=%d, pts=%"PRId64, input_packet->stream_index, input_packet->pts);
            av_packet_free(&input_packet);
        }
    }

xc_done:
    elv_dbg("av_read_frame() rc=%d", rc);

    txctx->stop = 1;
    elv_channel_close(txctx->vc);
    elv_channel_close(txctx->ac);
    pthread_join(txctx->vthread_id, NULL);
    pthread_join(txctx->athread_id, NULL);

    /*
     * Flush all frames, first flush decoder buffers, then encoder buffers by passing NULL frame.
     */
    if (params->tx_type & tx_video)
        flush_decoder(decoder_context, encoder_context, encoder_context->video_stream_index, params, debug_frame_level);
    if (params->tx_type & tx_audio)
        flush_decoder(decoder_context, encoder_context, encoder_context->audio_stream_index[0], params, debug_frame_level);
    if (params->tx_type & tx_audio_join || params->tx_type & tx_audio_merge) {
        for (int i=0; i<decoder_context->n_audio; i++)
            flush_decoder(decoder_context, encoder_context, decoder_context->audio_stream_index[i], params, debug_frame_level);
    }

    if (!params->bypass_transcoding && (params->tx_type & tx_video))
        encode_frame(decoder_context, encoder_context, NULL, encoder_context->video_stream_index, params, debug_frame_level);
    if (!params->bypass_transcoding && params->tx_type & tx_audio)
        encode_frame(decoder_context, encoder_context, NULL, encoder_context->audio_stream_index[0], params, debug_frame_level);

    dump_trackers(decoder_context->format_context, encoder_context->format_context);

    av_packet_free(&input_packet);
    av_frame_free(&input_frame);
    av_frame_free(&filt_frame);

    if (params->tx_type & tx_video)
        av_write_trailer(encoder_context->format_context);
    if (params->tx_type & tx_audio)
        av_write_trailer(encoder_context->format_context2);

    elv_log("avpipe_xc done last video_pts=%"PRId64" audio_pts=%"PRId64
        " video_input_start_pts=%"PRId64" audio_input_start_pts=%"PRId64
        " video_last_dts=%"PRId64" audio_last_dts="PRId64
        " last_pts_read=%"PRId64" last_pts_read2=%"PRId64
        " video_pts_sent_encode=%"PRId64" audio_pts_sent_encode=%"PRId64
        " last_pts_encoded=%"PRId64" last_pts_encoded2=%"PRId64,
        encoder_context->video_pts,
        encoder_context->audio_pts,
        encoder_context->video_input_start_pts,
        encoder_context->audio_input_start_pts,
        encoder_context->video_last_dts,
        encoder_context->audio_last_dts,
        encoder_context->video_last_pts_read,
        encoder_context->audio_last_pts_read,
        encoder_context->video_last_pts_sent_encode,
        encoder_context->audio_last_pts_sent_encode,
        encoder_context->video_last_pts_encoded,
        encoder_context->audio_last_pts_encoded);

    decoder_context->stopped = 1;
    encoder_context->stopped = 1;

    if (decoder_context->cancelled) {
        elv_err("transcoding session cancelled, handle=%d", txctx->handle);
        return -1;
    }

    return 0;
}

typedef struct channel_layout_info_t {
    const char *name;
    int         nb_channels;
    uint64_t    layout;
} channel_layout_info_t;

typedef struct channel_name_t {
    const char *name;
    const char *description;
} channel_name_t;

const channel_layout_info_t channel_layout_map[] = {
    { "mono",        1,  AV_CH_LAYOUT_MONO },
    { "stereo",      2,  AV_CH_LAYOUT_STEREO },
    { "2.1",         3,  AV_CH_LAYOUT_2POINT1 },
    { "3.0",         3,  AV_CH_LAYOUT_SURROUND },
    { "3.0(back)",   3,  AV_CH_LAYOUT_2_1 },
    { "4.0",         4,  AV_CH_LAYOUT_4POINT0 },
    { "quad",        4,  AV_CH_LAYOUT_QUAD },
    { "quad(side)",  4,  AV_CH_LAYOUT_2_2 },
    { "3.1",         4,  AV_CH_LAYOUT_3POINT1 },
    { "5.0",         5,  AV_CH_LAYOUT_5POINT0_BACK },
    { "5.0(side)",   5,  AV_CH_LAYOUT_5POINT0 },
    { "4.1",         5,  AV_CH_LAYOUT_4POINT1 },
    { "5.1",         6,  AV_CH_LAYOUT_5POINT1_BACK },
    { "5.1(side)",   6,  AV_CH_LAYOUT_5POINT1 },
    { "6.0",         6,  AV_CH_LAYOUT_6POINT0 },
    { "6.0(front)",  6,  AV_CH_LAYOUT_6POINT0_FRONT },
    { "hexagonal",   6,  AV_CH_LAYOUT_HEXAGONAL },
    { "6.1",         7,  AV_CH_LAYOUT_6POINT1 },
    { "6.1(back)",   7,  AV_CH_LAYOUT_6POINT1_BACK },
    { "6.1(front)",  7,  AV_CH_LAYOUT_6POINT1_FRONT },
    { "7.0",         7,  AV_CH_LAYOUT_7POINT0 },
    { "7.0(front)",  7,  AV_CH_LAYOUT_7POINT0_FRONT },
    { "7.1",         8,  AV_CH_LAYOUT_7POINT1 },
    { "7.1(wide)",   8,  AV_CH_LAYOUT_7POINT1_WIDE_BACK },
    { "7.1(wide-side)",   8,  AV_CH_LAYOUT_7POINT1_WIDE },
    { "octagonal",   8,  AV_CH_LAYOUT_OCTAGONAL },
    { "hexadecagonal", 16, AV_CH_LAYOUT_HEXADECAGONAL },
    { "downmix",     2,  AV_CH_LAYOUT_STEREO_DOWNMIX, },
};

static const struct channel_name_t channel_names[] = {
     [0] = { "FL",        "front left"            },
     [1] = { "FR",        "front right"           },
     [2] = { "FC",        "front center"          },
     [3] = { "LFE",       "low frequency"         },
     [4] = { "BL",        "back left"             },
     [5] = { "BR",        "back right"            },
     [6] = { "FLC",       "front left-of-center"  },
     [7] = { "FRC",       "front right-of-center" },
     [8] = { "BC",        "back center"           },
     [9] = { "SL",        "side left"             },
    [10] = { "SR",        "side right"            },
    [11] = { "TC",        "top center"            },
    [12] = { "TFL",       "top front left"        },
    [13] = { "TFC",       "top front center"      },
    [14] = { "TFR",       "top front right"       },
    [15] = { "TBL",       "top back left"         },
    [16] = { "TBC",       "top back center"       },
    [17] = { "TBR",       "top back right"        },
    [29] = { "DL",        "downmix left"          },
    [30] = { "DR",        "downmix right"         },
    [31] = { "WL",        "wide left"             },
    [32] = { "WR",        "wide right"            },
    [33] = { "SDL",       "surround direct left"  },
    [34] = { "SDR",       "surround direct right" },
    [35] = { "LFE2",      "low frequency 2"       },
};

static const channel_layout_info_t*
get_channel_layout_info(
    int nb_channels,
    int channel_layout)
{
    for (int i = 0; i < sizeof(channel_layout_map)/sizeof(channel_layout_map[0]); i++) {
        if (nb_channels == channel_layout_map[i].nb_channels &&
            channel_layout_map[i].layout == channel_layout)
            return &channel_layout_map[i];
    }

    return NULL;
}

static const char*
get_channel_name(
    int channel_layout)
{
    if (channel_layout < 0 || channel_layout >= sizeof(channel_names)/sizeof(channel_names[0]))
        return NULL;
    return channel_names[channel_layout].name;
}

const char*
avpipe_channel_name(
    int nb_channels,
    int channel_layout)
{
    const channel_layout_info_t *info = get_channel_layout_info(nb_channels, channel_layout);
    if (info != NULL) {
        return info->name;
    }

    for (int i = 0, ch = 0; i < 64; i++) {
        if ((channel_layout & (UINT64_C(1) << i))) {
            const char *name = get_channel_name(i);
            if (name)
                return name;
            ch++;
        }
    }

    return "";
}

static const char*
get_tx_type_name(
    tx_type_t tx_type)
{
    switch (tx_type) {
    case tx_video:
        return "tx_video";
    case tx_audio:
        return "tx_audio";
    case tx_all:
        return "tx_all";
    case tx_audio_join:
        return "tx_audio_join";
    case tx_audio_pan:
        return "tx_audio_pan";
    case tx_audio_merge:
        return "tx_audio_merge";
    default:
        return "none";
    }

    return "none";
}

int
avpipe_probe(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    int seekable,
    txprobe_t **txprobe)
{
    coderctx_t decoder_ctx;
    stream_info_t *stream_probes, *stream_probes_ptr;
    txprobe_t *probe;
    int rc;

    if (!in_handlers) {
        elv_err("avpipe_probe NULL handlers");
        rc = -1;
        goto avpipe_probe_end;
    }

    memset(&decoder_ctx, 0, sizeof(coderctx_t));

    if (prepare_decoder(&decoder_ctx, in_handlers, inctx, NULL, seekable)) {
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
    probe = (txprobe_t *)calloc(1, sizeof(txprobe_t));
    stream_probes = (stream_info_t *)calloc(1, sizeof(stream_info_t)*nb_streams);
    for (int i=0; i<nb_streams; i++) {
        AVStream *s = decoder_ctx.format_context->streams[i];
        AVCodecContext *codec_context = decoder_ctx.codec_context[i];
        AVCodec *codec = decoder_ctx.codec[i];
        AVRational sar, dar;

        //if (!codec || !codec_context || (codec->type != AVMEDIA_TYPE_VIDEO && codec->type != AVMEDIA_TYPE_AUDIO)) {
        if (!codec_context) {
            nb_skipped_streams++;
            continue;
        }
        stream_probes_ptr = &stream_probes[i-nb_skipped_streams];
        stream_probes_ptr->stream_index = i;
        stream_probes_ptr->stream_id = s->id;
        if (codec) {
            stream_probes_ptr->codec_type = codec->type;
            stream_probes_ptr->codec_id = codec->id;
            strncpy(stream_probes_ptr->codec_name, codec->name, MAX_CODEC_NAME);
        } else {
            stream_probes_ptr->codec_type = decoder_ctx.format_context->streams[i]->codecpar->codec_type;
        }
        stream_probes_ptr->codec_name[MAX_CODEC_NAME] = '\0';
        stream_probes_ptr->duration_ts = s->duration;
        stream_probes_ptr->time_base = s->time_base;
        stream_probes_ptr->nb_frames = s->nb_frames;
        stream_probes_ptr->start_time = s->start_time;
        stream_probes_ptr->avg_frame_rate = s->avg_frame_rate;

        // Find sample asperct ratio and diplay aspect ratio
        sar = av_guess_sample_aspect_ratio(decoder_ctx.format_context, s, NULL);
        if (sar.num) {
            stream_probes_ptr->sample_aspect_ratio = sar;
            av_reduce(&dar.num, &dar.den,
                      codec_context->width  * sar.num,
                      codec_context->height * sar.den,
                      1024*1024);
            stream_probes_ptr->display_aspect_ratio = dar;
        } else {
            stream_probes_ptr->sample_aspect_ratio = codec_context->sample_aspect_ratio;
            stream_probes_ptr->display_aspect_ratio = s->display_aspect_ratio;
        }

        stream_probes_ptr->frame_rate = s->r_frame_rate;
        stream_probes_ptr->ticks_per_frame = codec_context->ticks_per_frame;
        stream_probes_ptr->bit_rate = codec_context->bit_rate;
        stream_probes_ptr->has_b_frames = codec_context->has_b_frames;
        stream_probes_ptr->sample_rate = codec_context->sample_rate;
        stream_probes_ptr->channels = codec_context->channels;
        if (codec && codec->type == AVMEDIA_TYPE_AUDIO)
            stream_probes_ptr->channel_layout = codec_context->channel_layout;
        else
            stream_probes_ptr->channel_layout = -1;
        stream_probes_ptr->width = codec_context->width;
        stream_probes_ptr->height = codec_context->height;
        stream_probes_ptr->pix_fmt = codec_context->pix_fmt;
        stream_probes_ptr->field_order = codec_context->field_order;
        stream_probes_ptr->profile = codec_context->profile;
        stream_probes_ptr->level = codec_context->level;

        if (probe->container_info.duration < ((float)stream_probes_ptr->duration_ts)/stream_probes_ptr->time_base.den)
            probe->container_info.duration = ((float)stream_probes_ptr->duration_ts)/stream_probes_ptr->time_base.den;
    }

    probe->stream_info = stream_probes;
    probe->container_info.format_name = strdup(decoder_ctx.format_context->iformat->name);
    *txprobe = probe;
    rc = nb_streams - nb_skipped_streams;

avpipe_probe_end:
    return rc;
}

static int
check_params(
    txparams_t *params)
{
    if (params->stream_id >= 0 && (params->tx_type != tx_none || params->n_audio > 0)) {
        elv_err("Incompatible params, stream_id=%d, tx_type=%d, n_audio=%d",
            params->stream_id, params->tx_type, params->n_audio);
        return -1;
    }
    
    // By default transcode 'everything' if streamId < 0
    if (params->tx_type == tx_none && params->stream_id < 0) {
        params->tx_type = tx_all;
    }

    if (params->start_pts < 0) {
        elv_err("Start PTS can not be negative");
        return -1;
    }

    if (params->watermark_text != NULL && (strlen(params->watermark_text) > (WATERMARK_STRING_SZ-1))){
        elv_err("Watermark too large");
        return -1;
    }

    /* Infer rc parameters */
    if (params->video_bitrate > 0) {
        params->rc_max_rate = params->video_bitrate * 1;
        params->rc_buffer_size = params->video_bitrate;
    }

    if (params->bitdepth == 0) {
        params->bitdepth = 8;
        elv_log("Set bitdepth=%d", params->bitdepth);
    }

    if (params->tx_type & tx_audio &&
        params->seg_duration <= 0 &&
        params->audio_seg_duration_ts <= 0) {
        elv_err("Segment duration is not set for audio (invalid seg_duration and audio_seg_duration_ts)");
        return -1;
    }

    if (params->tx_type & tx_video &&
        params->seg_duration <= 0 &&
        params->video_seg_duration_ts <= 0) {
        elv_err("Segment duration is not set for video (invalid seg_duration and video_seg_duration_ts)");
        return -1;
    }

    if (params->stream_id >=0 &&
        params->seg_duration <= 0) {
        elv_err("Segment duration is not set for stream id=%d", params->stream_id);
        return -1;
    }

    if (params->tx_type == tx_audio_merge) {
        elv_err("Audio merge is not supported yet.");
        return -1;
    }

    if (params->tx_type != tx_audio_join &&
        params->tx_type != tx_audio_pan
        && params->n_audio > 1) {
        elv_err("Invalid number of audio streams, n_audio=%d", params->n_audio);
        return -1;
    }

    /* Set n_audio to zero if n_audio < 0 or tx_type == tx_video */
    if (params->n_audio < 0 ||
        params->tx_type == tx_video)
        params->n_audio = 0;

    if ((params->tx_type == tx_audio_join ||
        params->tx_type == tx_audio_merge) &&
        params->n_audio < 2) {
        elv_err("Insufficient audio indexes, n_audio=%d, tx_type=%d", params->n_audio, params->tx_type);
        return -1;
    }

    return 0;
}

int
avpipe_init(
    txctx_t **txctx,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    avpipe_io_handler_t *out_handlers,
    txparams_t *p,
    char *url)
{
    txctx_t *p_txctx = (txctx_t *) calloc(1, sizeof(txctx_t));
    txparams_t *params;

    if (!txctx) {
        elv_err("Trancoding context is NULL");
        goto avpipe_init_failed;
    }

    if (!p) {
        elv_err("Parameters are not set");
        goto avpipe_init_failed;
    }

    params = (txparams_t *) calloc(1, sizeof(txparams_t));
    *params = *p;
    p_txctx->params = params;

    if (!params->format ||
        (strcmp(params->format, "dash") &&
         strcmp(params->format, "hls") &&
         strcmp(params->format, "mp4") &&
         strcmp(params->format, "fmp4") &&
         strcmp(params->format, "segment") &&
         strcmp(params->format, "fmp4-segment"))) {
        elv_err("Output format can be only \"dash\", \"hls\", \"mp4\", \"fmp4\", \"segment\", or \"fmp4-segment\"");
        goto avpipe_init_failed;
    }

    char buf[1024];
    char audio_index_str[256];
    char index_str[10];

    audio_index_str[0] = '\0';
    for (int i=0; i<params->n_audio; i++) {
        snprintf(index_str, 10, "%d", params->audio_index[i]);
        strcat(audio_index_str, index_str);
        if (i < params->n_audio-1)
            strcat(audio_index_str, ",");
    }

    sprintf(buf,
        "stream_id=%d "
        "url=%s "
        "version=%s "
        "bypass=%d "
        "tx_type=%s "
        "format=%s "
        "seekable=%d "
        "start_time_ts=%"PRId64" "
        "start_pts=%"PRId64" "
        "duration_ts=%"PRId64" "
        "start_segment_str=%s "
        "video_bitrate=%d "
        "audio_bitrate=%d "
        "sample_rate=%d "
        "crf_str=%s "
        "preset=%s "
        "rc_max_rate=%d "
        "rc_buffer_size=%d "
        "video_seg_duration_ts=%"PRId64" "
        "audio_seg_duration_ts=%"PRId64" "
        "seg_duration=%s "
        "force_keyint=%d "
        "force_equal_fduration=%d "
        "ecodec=%s "
        "ecodec2=%s "
        "dcodec=%s "
        "dcodec2=%s "
        "gpu_index=%d "
        "enc_height=%d "
        "enc_width=%d "
        "crypt_iv=%s "
        "crypt_key=%s "
        "crypt_kid=%s "
        "crypt_key_url=%s "
        "crypt_scheme=%d "
        "n_audio=%d "
        "audio_index=%s "
        "sync_audio_to_stream_id=%d "
        "audio_fill_gap=%d "
        "wm_overlay_type=%d "
        "wm_overlay_len=%d "
        "bitdepth=%d "
        "listen=%d "
        "max_cll=\"%s\" "
        "master_display=\"%s\" "
        "filter_descriptor=\"%s\"",
        params->stream_id, url,
        avpipe_version(),
        params->bypass_transcoding, get_tx_type_name(params->tx_type), params->format,
        params->seekable, params->start_time_ts, params->start_pts, params->duration_ts, params->start_segment_str,
        params->video_bitrate, params->audio_bitrate, params->sample_rate, params->crf_str, params->preset,
        params->rc_max_rate, params->rc_buffer_size, params->video_seg_duration_ts, params->audio_seg_duration_ts,
        params->seg_duration, params->force_keyint, params->force_equal_fduration, params->ecodec,
        params->ecodec2, params->dcodec, params->dcodec2, params->gpu_index, params->enc_height, params->enc_width,
        params->crypt_iv, params->crypt_key, params->crypt_kid, params->crypt_key_url, params->crypt_scheme,
        params->n_audio, audio_index_str, params->sync_audio_to_stream_id, params->audio_fill_gap,
        params->watermark_overlay_type, params->watermark_overlay_len, params->bitdepth, params->listen,
        params->max_cll ? params->max_cll : "", params->master_display ? params->master_display : "",
        params->filter_descriptor);
    elv_log("AVPIPE XCPARAMS %s", buf);

    if (check_params(params) < 0)
        goto avpipe_init_failed;

    if (prepare_decoder(&p_txctx->decoder_ctx, in_handlers, inctx, params, params->seekable)) {
        elv_err("Failure in preparing decoder");
        goto avpipe_init_failed;
    }

    if (prepare_encoder(&p_txctx->encoder_ctx, &p_txctx->decoder_ctx, out_handlers, inctx, params)) {
        elv_err("Failure in preparing output");
        goto avpipe_init_failed;
    }

    p_txctx->inctx = inctx;
    elv_channel_init(&p_txctx->vc, 10000);
    elv_channel_init(&p_txctx->ac, 10000);

    /* Create threads for decoder and encoder */
    pthread_create(&p_txctx->vthread_id, NULL, transcode_video_func, p_txctx);
    pthread_create(&p_txctx->athread_id, NULL, transcode_audio_func, p_txctx);
    *txctx = p_txctx;

    return 0;

avpipe_init_failed:
    if (txctx)
        *txctx = NULL;
    avpipe_fini(&p_txctx);
    return -1;
}

static void
avpipe_free_params(
    txctx_t *txctx)
{
    txparams_t *params = txctx->params;

    if (!params)
        return;

    free(params->format);
    free(params->start_segment_str);
    free(params->crf_str);
    free(params->preset);
    free(params->seg_duration);
    free(params->ecodec);
    free(params->ecodec2);
    free(params->dcodec);
    free(params->dcodec2);
    free(params->crypt_iv);
    free(params->crypt_key);
    free(params->crypt_kid);
    free(params->crypt_key_url);
    free(params->watermark_text);
    free(params->watermark_xloc);
    free(params->watermark_yloc);
    free(params->watermark_font_color);
    free(params->watermark_shadow_color);
    free(params->watermark_overlay);
    free(params->mux_spec);
    free(params->filter_descriptor);
    free(params);
    txctx->params = NULL;
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
        if ((*txctx)->inctx && strncmp((*txctx)->inctx->url, "rtmp", 4)) {
            AVIOContext *avioctx = (AVIOContext *) decoder_context->format_context->pb;
            if (avioctx) {
                av_freep(&avioctx->buffer);
                av_freep(&avioctx);
            }
        }
    }

    /* Corresponds to avformat_open_input */
    if (decoder_context && decoder_context->format_context)
        avformat_close_input(&decoder_context->format_context);
    //avformat_free_context(decoder_context->format_context);

    /* Free filter graph resources */
    if (decoder_context && decoder_context->video_filter_graph)
        avfilter_graph_free(&decoder_context->video_filter_graph);
    if (decoder_context && decoder_context->audio_filter_graph)
        avfilter_graph_free(&decoder_context->audio_filter_graph);

    //avformat_close_input(&encoder_context->format_context);
    if (encoder_context && encoder_context->format_context) {
        free(encoder_context->format_context->avpipe_opaque);
        avformat_free_context(encoder_context->format_context);
    }
    if (encoder_context && encoder_context->format_context2) {
        free(encoder_context->format_context2->avpipe_opaque);
        avformat_free_context(encoder_context->format_context2);
    }

    for (int i=0; i<MAX_STREAMS; i++) {
        if (decoder_context->codec_context[i]) {
            /* Corresponds to avcodec_open2() */
            avcodec_close(decoder_context->codec_context[i]);
            avcodec_free_context(&decoder_context->codec_context[i]);
        }

        if (encoder_context->codec_context[i]) {
            /* Corresponds to avcodec_open2() */
            avcodec_close(encoder_context->codec_context[i]);
            avcodec_free_context(&encoder_context->codec_context[i]);
        }
    }

    if ((*txctx)->params && !strcmp((*txctx)->params->ecodec2, "aac")) {
        av_audio_fifo_free(decoder_context->fifo);
        swr_free(&decoder_context->resampler_context);
    }

    free((*txctx)->inctx);
    elv_channel_fini(&((*txctx)->vc));
    elv_channel_fini(&((*txctx)->ac));
    avpipe_free_params(*txctx);
    free(*txctx);
    *txctx = NULL;

    return 0;
}

char *
avpipe_version()
{
    static char version_str[128];

    if (version_str[0] != '\0')
        return version_str;

    snprintf(version_str, sizeof(version_str), "%d.%d@%s", AVPIPE_MAJOR_VERSION, AVPIPE_MINOR_VERSION, VERSION);

    return version_str;
}
