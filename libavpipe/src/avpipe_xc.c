/*
 * Audio/video transcoding pipeline
 *
 * Build:
 *
 * . init-env.sh <PATH_TO_CONTENT_FABRIC>
 * make
 *
 */

#include <libavutil/log.h>
#include "libavutil/audio_fifo.h"
#include <libswscale/swscale.h>
#include <libavutil/imgutils.h>
#include <libavutil/display.h>

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"
#include "elv_time.h"
#include "url_parser.h"
#include "avpipe_version.h"
#include "base64.h"
#include "scte35.h"

#include <stdio.h>
#include <fcntl.h>
#include <assert.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/uio.h>
#include <unistd.h>
#include <stdlib.h>
#include <pthread.h>

#define AUDIO_BUF_SIZE              (128*1024)
#define INPUT_IS_SEEKABLE           0

#define MPEGTS_THREAD_COUNT         16
#define DEFAULT_THREAD_COUNT        8
#define WATERMARK_STRING_SZ         (64*1024)   /* Max length of watermark text */
#define FILTER_STRING_SZ            (1024 + WATERMARK_STRING_SZ)
#define DEFAULT_FRAME_INTERVAL_S    10

#define DEFAULT_ACC_SAMPLE_RATE     48000

extern int
init_video_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

extern int
init_audio_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

int
init_audio_pan_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context);

int
init_audio_merge_pan_filters(
    const char *filters_descr,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context);

extern int
init_audio_join_filters(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params);

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

static const char*
get_channel_name(
    int channel_layout);

static int
get_channel_layout_for_encoder(
    int);

const char*
avpipe_channel_layout_name(
    int channel_layout);

//#define USE_RESAMPLE_AAC
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

static int
is_protocol(
    coderctx_t *decoder_context)
{
    if (decoder_context->is_mpegts || decoder_context->is_rtmp || decoder_context->is_srt)
        return 1;

    return 0;
}

int
prepare_input(
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    coderctx_t *decoder_context,
    int seekable)
{
    unsigned char *bufin;
    AVIOContext *avioctx;
    int bufin_sz = AVIO_IN_BUF_SIZE;

    /* For RTMP protocol don't create input callbacks */
    decoder_context->is_rtmp = 0;
    decoder_context->is_srt = 0;
    if (inctx->url && !strncmp(inctx->url, "rtmp://", 7)) {
        decoder_context->is_rtmp = 1;
        return 0;
    }

    if (inctx->url && !strncmp(inctx->url, "srt://", 6)) {
        decoder_context->is_srt = 1;
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

/* Calculate final output timebase based on the codec timebase by replicating
 * the logic in the ffmpeg muxer: multiply by 2 until greater than 10,000
 */
static int
calc_timebase(
    xcparams_t *params,
    int is_video,
    int timebase)
{
    if (timebase <= 0) {
        elv_err("calc_timebase invalid timebase=%d", timebase);
        return timebase;
    }

    if (is_video && params->video_time_base > 0)
        timebase = params->video_time_base;

    while (timebase < TIMEBASE_THRESHOLD)
        timebase *= 2;

    return timebase;
}

static int
selected_audio_index(
    xcparams_t *params,
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
    int stream_index)
{
    if (decoder_context->n_audio <= 0)
        return -1;

    for (int i=0; i<decoder_context->n_audio; i++) {
        if (decoder_context->audio_stream_index[i] == stream_index)
            return i;
    }

    return -1;
}

static int
prepare_decoder(
    coderctx_t *decoder_context,
    avpipe_io_handler_t *in_handlers,
    ioctx_t *inctx,
    xcparams_t *params,
    int seekable)
{
    int rc;
    decoder_context->video_last_dts = AV_NOPTS_VALUE;
    int stream_id_index = -1;
    int sync_id_index = -1;     // Index of the video stream used for audio sync
    char *url = params ? params->url : "";

    decoder_context->in_handlers = in_handlers;
    decoder_context->inctx = inctx;
    decoder_context->video_stream_index = -1;
    decoder_context->data_scte35_stream_index = -1;
    for (int j=0; j<MAX_STREAMS; j++) {
        decoder_context->audio_stream_index[j] = -1;
        decoder_context->audio_last_dts[j] = AV_NOPTS_VALUE;
    }

    decoder_context->format_context = avformat_alloc_context();
    if (!decoder_context->format_context) {
        elv_err("Could not allocate memory for Format Context, url=%s", url);
        return eav_mem_alloc;
    }

    /* Set our custom reader */
    prepare_input(in_handlers, inctx, decoder_context, seekable);

    AVDictionary *opts = NULL;
    if (params && params->listen && (decoder_context->is_rtmp || decoder_context->is_srt))
        av_dict_set(&opts, "listen", "1" , 0);

    if (decoder_context->is_rtmp &&
        params->listen &&
        params->connection_timeout > 0) {
        char timeout[32];
        sprintf(timeout, "%d", params->connection_timeout);
        av_dict_set(&opts, "timeout", timeout, 0);
    }

    /* Allocate AVFormatContext in format_context and find input file format */
    rc = avformat_open_input(&decoder_context->format_context, inctx->url, NULL, &opts);
    if (rc != 0) {
        elv_err("Could not open input file, err=%d, url=%s", rc, url);
        return eav_open_input;
    }

    /* Retrieve stream information */
    if (avformat_find_stream_info(decoder_context->format_context,  NULL) < 0) {
        elv_err("Could not get input stream info, url=%s", url);
        return eav_stream_info;
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
            if (params && (params->xc_type & xc_video) && params->stream_id < 0 && decoder_context->video_stream_index < 0)
                decoder_context->video_stream_index = i;
            elv_dbg("VIDEO STREAM %d, codec_id=%s, stream_id=%d, timebase=%d, xc_type=%d, url=%s",
                i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id), decoder_context->stream[i]->id,
                decoder_context->stream[i]->time_base.den, params ? params->xc_type : xc_none, url);
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
            elv_dbg("AUDIO STREAM %d, codec_id=%s, stream_id=%d, timebase=%d, xc_type=%d, channels=%d, url=%s",
                i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id), decoder_context->stream[i]->id,
                decoder_context->stream[i]->time_base.den, params ? params->xc_type : xc_none,
                decoder_context->codec_parameters[i]->channels, url);

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

            switch (decoder_context->codec_parameters[i]->codec_id) {
            case AV_CODEC_ID_SCTE_35:
                decoder_context->data_scte35_stream_index = i;
                elv_dbg("DATA STREAM SCTE-35 %d, codec_id=%s, stream_id=%d, url=%s",
                    i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id),
                    decoder_context->stream[i]->id, url);
                break;
            default:
                // Unrecognized data stream
                decoder_context->data_stream_index = i;
                elv_dbg("DATA STREAM UNRECOGNIZED %d, codec_id=%s, stream_id=%d, url=%s",
                    i, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id),
                    decoder_context->stream[i]->id, url);
                break;
            }

            break;

        default:
            decoder_context->codec[i] = NULL;
            elv_dbg("UNKNOWN STREAM type=%d, url=%s",
                decoder_context->format_context->streams[i]->codecpar->codec_type, url);
            continue;
        }

        /* Is this the stream selected for transcoding? */
        int selected_stream = 0;
        int this_stream_id =  decoder_context->is_rtmp ? i : decoder_context->stream[i]->id;
        if (params && this_stream_id == params->stream_id) {
            elv_log("STREAM MATCH stream_id=%d, stream_index=%d, xc_type=%d, url=%s",
                params->stream_id, i, params->xc_type, url);
            stream_id_index = i;
            selected_stream = 1;
        }

        if (params && this_stream_id == params->sync_audio_to_stream_id) {
            if (decoder_context->stream[i]->codecpar->codec_type != AVMEDIA_TYPE_VIDEO) {
                elv_err("Syncing to non-video stream is not possible, sync_audio_to_stream_id=%d, url=%s",
                    params->sync_audio_to_stream_id, url);
                return eav_stream_index;
            }
            elv_log("STREAM MATCH sync_stream_id=%d stream_index=%d, url=%s",
                params->sync_audio_to_stream_id, i, url);
            sync_id_index = i;
        }

        /* If stream ID is not set - match audio_index */
        if (params && params->stream_id < 0 &&
            params->xc_type & xc_audio &&
            (selected_audio_index(params, i) >= 0 ||
                (decoder_context->n_audio > 0 && decoder_context->audio_stream_index[decoder_context->n_audio-1] == i))) {
            selected_stream = 1;
        }

        /*
         * Find decoder and initialize decoder context.
         * Pick params->dcodec if this is the selected stream (stream_id or audio_index)
         */
        if (params != NULL && params->dcodec != NULL && params->dcodec[0] != '\0' && 
            decoder_context->format_context->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
            elv_log("STREAM SELECTED this_stream_id=%d, id=%d idx=%d xc_type=%d dcodec=%s, url=%s",
                this_stream_id, decoder_context->stream[i]->id, i, params->xc_type, params->dcodec, url);
            decoder_context->codec[i] = avcodec_find_decoder_by_name(params->dcodec);
        } else if (params != NULL && params->dcodec2 != NULL && params->dcodec2[0] != '\0' && selected_stream &&
            decoder_context->format_context->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
            elv_log("STREAM SELECTED this_stream_id=%d, id=%d idx=%d xc_type=%d dcodec2=%s, url=%s",
                this_stream_id, decoder_context->stream[i]->id, i, params->xc_type, params->dcodec2, url);
            decoder_context->codec[i] = avcodec_find_decoder_by_name(params->dcodec2);
        } else {
            decoder_context->codec[i] = avcodec_find_decoder(decoder_context->codec_parameters[i]->codec_id);
        }

        if (decoder_context->codec_parameters[i]->codec_type != AVMEDIA_TYPE_DATA && !decoder_context->codec[i]) {
            elv_err("Unsupported decoder codec param=%s, codec_id=%d, url=%s",
                params ? params->dcodec : "", decoder_context->codec_parameters[i]->codec_id, url);
            return eav_codec_param;
        }

        decoder_context->codec_context[i] = avcodec_alloc_context3(decoder_context->codec[i]);
        if (!decoder_context->codec_context[i]) {
            elv_err("Failed to allocated memory for AVCodecContext, url=%s", url);
            return eav_mem_alloc;
        }

        if (avcodec_parameters_to_context(decoder_context->codec_context[i], decoder_context->codec_parameters[i]) < 0) {
            elv_err("Failed to copy codec params to codec context, url=%s", url);
            return eav_codec_param;
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
            elv_err("Failed to open codec through avcodec_open2, err=%d, param=%s, codec_id=%s, url=%s",
                rc, params->dcodec, avcodec_get_name(decoder_context->codec_parameters[i]->codec_id), url);
            return eav_open_codec;
        }

        elv_log("Input stream=%d pixel_format=%s, timebase=%d, sample_fmt=%s, frame_size=%d, url=%s",
            i,
            av_get_pix_fmt_name(decoder_context->codec_context[i]->pix_fmt),
            decoder_context->stream[i]->time_base.den,
            av_get_sample_fmt_name(decoder_context->codec_context[i]->sample_fmt),
            decoder_context->codec_context[i]->frame_size, url);

        /* Video - set context parameters manually */
        /* Setting the frame_rate here causes slight changes to rates - leaving it unset works perfectly
          decoder_context->codec_context[i]->framerate = av_guess_frame_rate(
            decoder_context->format_context, decoder_context->format_context->streams[i], NULL);
        */

        /*
         * Force codec timebase to be the same as the input stream.  This is not required in principle but
         * downstream logic relies on it.
         *
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
        elv_err("Invalid stream_id=%d, url=%s", params->stream_id, url);
        return eav_param;
    }

    if (stream_id_index >= 0) {
        if (decoder_context->format_context->streams[stream_id_index]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO) {
            decoder_context->video_stream_index = stream_id_index;
            params->xc_type = xc_video;
        } else if (decoder_context->format_context->streams[stream_id_index]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO) {
            decoder_context->audio_stream_index[decoder_context->n_audio] = stream_id_index;
            decoder_context->n_audio++;
            params->xc_type = xc_audio;

            if (sync_id_index >= 0)
                decoder_context->video_stream_index = sync_id_index;
        }
    } else if (params && (params->n_audio == 1) &&
            (params->audio_index[0] < decoder_context->format_context->nb_streams)) {
        decoder_context->audio_stream_index[0] = params->audio_index[0];
        decoder_context->n_audio = 1;

    }
    elv_dbg("prepare_decoder xc_type=%d, video_stream_index=%d, audio_stream_index=%d, n_audio=%d, nb_streams=%d, url=%s",
        params ? params->xc_type : 0,
        decoder_context->video_stream_index,
        decoder_context->audio_stream_index[decoder_context->n_audio-1],
        decoder_context->n_audio,
        decoder_context->format_context->nb_streams,
        url);

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
        if (params->xc_type & xc_video) {
            if (decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num == 0 ||
            (decoder_context->stream[decoder_context->video_stream_index]->time_base.den *
            decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.den %
            decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num != 0)) {
                elv_err("Can not force equal frame duration, timebase=%d, frame_rate=%d/%d, url=%s",
                    decoder_context->stream[decoder_context->video_stream_index]->time_base.den,
                    decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.den,
                    decoder_context->format_context->streams[decoder_context->video_stream_index]->r_frame_rate.num,
                    url);
                return eav_timebase;
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
    xcparams_t *params,
    int stream_index,
    int timebase)
{
    int i;

    if (timebase <= 0) {
        elv_err("Setting encoder options failed, invalid timebase=%d (check encoding params), url=%s",
            timebase, params->url);
        return eav_timebase;
    }

    if (!strcmp(params->format, "fmp4")) {
        if (stream_index == decoder_context->video_stream_index)
            av_opt_set(encoder_context->format_context->priv_data, "movflags", "frag_every_frame", 0);
        if ((i = selected_decoded_audio(decoder_context, stream_index)) >= 0)
            av_opt_set(encoder_context->format_context2[i]->priv_data, "movflags", "frag_every_frame", 0);
    }

    // Segment duration (in ts) - notice it is set on the format context not codec
    if (params->audio_seg_duration_ts > 0 && (!strcmp(params->format, "dash") || !strcmp(params->format, "hls"))) {
        if ((i = selected_decoded_audio(decoder_context, stream_index)) >= 0)
            av_opt_set_int(encoder_context->format_context2[i]->priv_data, "seg_duration_ts", params->audio_seg_duration_ts,
                AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    }

    if (params->video_seg_duration_ts > 0 && (!strcmp(params->format, "dash") || !strcmp(params->format, "hls"))) {
        if (stream_index == decoder_context->video_stream_index)
            av_opt_set_int(encoder_context->format_context->priv_data, "seg_duration_ts", params->video_seg_duration_ts,
                AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
    }

    if ((i = selected_decoded_audio(decoder_context, stream_index)) >= 0) {
        if (!(params->xc_type & xc_audio)) {
            elv_err("Failed to set audio encoder options, stream_index=%d, xc_type=%d, url=%s",
                stream_index, params->xc_type, params->url);
            return eav_param;
        }
        av_opt_set_int(encoder_context->format_context2[i]->priv_data, "start_fragment_index", params->start_fragment_index,
            AV_OPT_FLAG_ENCODING_PARAM | AV_OPT_SEARCH_CHILDREN);
        av_opt_set(encoder_context->format_context2[i]->priv_data, "start_segment", params->start_segment_str, 0);
    }

    if (stream_index == decoder_context->video_stream_index) {
        if (!(params->xc_type & xc_video)) {
            elv_err("Failed to set video encoder options, stream_index=%d, xc_type=%d, url=%s",
                stream_index, params->xc_type, params->url);
            return eav_param;
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
                timebase = calc_timebase(params, 1, timebase);
            seg_duration_ts = seg_duration * timebase;
        }
        if ((i = selected_decoded_audio(decoder_context, stream_index)) >= 0) {
            if (params->audio_seg_duration_ts > 0)
                seg_duration_ts = params->audio_seg_duration_ts;
            av_opt_set_int(encoder_context->format_context2[i]->priv_data, "segment_duration_ts", seg_duration_ts, 0);
            /* If audio_seg_duration_ts is not set, set it now */
            if (params->audio_seg_duration_ts <= 0)
                params->audio_seg_duration_ts = seg_duration_ts;
            elv_dbg("setting \"fmp4-segment\" audio segment_time to %s, seg_duration_ts=%"PRId64", url=%s",
                params->seg_duration, seg_duration_ts, params->url);
            av_opt_set(encoder_context->format_context2[i]->priv_data, "reset_timestamps", "on", 0);
        } 
        if (stream_index == decoder_context->video_stream_index) {
            if (params->video_seg_duration_ts > 0)
                seg_duration_ts = params->video_seg_duration_ts;
            av_opt_set_int(encoder_context->format_context->priv_data, "segment_duration_ts", seg_duration_ts, 0);
            /* If video_seg_duration_ts is not set, set it now */
            if (params->video_seg_duration_ts <= 0)
                params->video_seg_duration_ts = seg_duration_ts;
            elv_dbg("setting \"fmp4-segment\" video segment_time to %s, seg_duration_ts=%"PRId64", url=%s",
                params->seg_duration, seg_duration_ts, params->url);
            av_opt_set(encoder_context->format_context->priv_data, "reset_timestamps", "on", 0);
        }
        // If I set faststart in the flags then ffmpeg generates some zero size files, which I need to dig into it more (RM).
        // av_opt_set(encoder_context->format_context->priv_data, "segment_format_options", "movflags=faststart", 0);
        // So lets use flag_every_frame option instead.
        if (!strcmp(params->format, "fmp4-segment")) {
            if ((i = selected_decoded_audio(decoder_context, stream_index)) >= 0)
                av_opt_set(encoder_context->format_context2[i]->priv_data, "segment_format_options", "movflags=frag_every_frame", 0);
            if (stream_index == decoder_context->video_stream_index)
                av_opt_set(encoder_context->format_context->priv_data, "segment_format_options", "movflags=frag_every_frame", 0);
        }
    }

    return 0;
}

/*
 * Set H264 specific params profile, and level based on encoding height.
 */
static void
set_h264_params(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int index = decoder_context->video_stream_index;
    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];
    int framerate = 0;

    /* Codec level and profile must be set correctly per H264 spec */
    encoder_codec_context->profile = avpipe_h264_guess_profile(params->bitdepth,
        encoder_codec_context->width, encoder_codec_context->height);

    if (encoder_codec_context->framerate.den != 0)
        framerate = encoder_codec_context->framerate.num/encoder_codec_context->framerate.den;

    encoder_codec_context->level = avpipe_h264_guess_level(
                                                encoder_codec_context->profile,
                                                encoder_codec_context->bit_rate,
                                                framerate,
                                                encoder_codec_context->width,
                                                encoder_codec_context->height);
}

static void
set_h265_params(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int index = decoder_context->video_stream_index;
    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];

    /*
     * The Main profile allows for a bit depth of 8-bits per sample with 4:2:0 chroma sampling,
     * which is the most common type of video used with consumer devices
     * For HDR10 we need MAIN 10 that supports 10 bit profile.
     */
    if (params->bitdepth == 8) {
        av_opt_set(encoder_codec_context->priv_data, "profile", "main", 0);
    } else if (params->bitdepth == 10) {
        av_opt_set(encoder_codec_context->priv_data, "profile", "main10", 0);
        av_opt_set(encoder_codec_context->priv_data, "x265-params",
            "hdr-opt=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc", 0);
    } else {
        /* bitdepth == 12 */
        av_opt_set(encoder_codec_context->priv_data, "profile", "main12", 0);
        av_opt_set(encoder_codec_context->priv_data, "x265-params",
            "hdr-opt=1:repeat-headers=1:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc", 0);
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
     * Let X265 encoder picks the level automatically. Setting the level based on
     * resolution and framerate might pick higher level than what is needed.
     */
}

static void
set_netint_h264_params(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    char enc_params[256];
    int index = decoder_context->video_stream_index;
    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];
    AVStream *s = decoder_context->stream[decoder_context->video_stream_index];

    if (params->force_keyint > 0)
        sprintf(enc_params, "gopPresetIdx=2:lowDelay=1:frameRate=%d:frameRateDenom=%d:intraPeriod=%d",
            s->avg_frame_rate.num, s->avg_frame_rate.den, params->force_keyint);
    else
        sprintf(enc_params, "gopPresetIdx=2:lowDelay=1:frameRate=%d:frameRateDenom=%d",
            s->avg_frame_rate.num, s->avg_frame_rate.den);
    elv_dbg("set_netint_h264_params encoding params=%s, url=%s", enc_params, params->url);
    av_opt_set(encoder_codec_context->priv_data, "xcoder-params", enc_params, 0);
}

static void
set_netint_h265_params(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    char enc_params[256];
    int profile;
    int index = decoder_context->video_stream_index;
    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];
    AVStream *s = decoder_context->stream[decoder_context->video_stream_index];

    /*
     * netint only supports bitdepth of 8 and 10 for h265.
     * 1 = profile main, for bitdepth 8
     * 2 = profile main10, for bitdepth 10
     */
    if (params->bitdepth == 8)
        profile = 1;
    else
        profile = 2;
    sprintf(enc_params, "gopPresetIdx=2:lowDelay=1:profile=%d:frameRate=%d:frameRateDenom=%d",
        profile, s->avg_frame_rate.num, s->avg_frame_rate.den);
    elv_dbg("set_netint_h265_params encoding params=%s, url=%s", enc_params, params->url);
    av_opt_set(encoder_codec_context->priv_data, "xcoder-params", enc_params, 0);
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
    xcparams_t *params)
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

/*
    For nvidia let the encoder to pick the level, in some cases level is different from h264 encoder and
    avpipe_h264_guess_level() function would not pick the right level for nvidia.

    if (encoder_codec_context->framerate.den != 0)
        framerate = encoder_codec_context->framerate.num/encoder_codec_context->framerate.den;

    encoder_codec_context->level = avpipe_h264_guess_level(
                                                encoder_codec_context->profile,
                                                encoder_codec_context->bit_rate,
                                                framerate,
                                                encoder_codec_context->width,
                                                encoder_codec_context->height);
    av_opt_set_int(encoder_codec_context->priv_data, "level", encoder_codec_context->level, 0);
*/

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
    xcparams_t *params)
{
    if (encoder_codec_context->codec_id == AV_CODEC_ID_MJPEG) {
        //                               AV_PIX_FMT_YUV420P does not work
        encoder_codec_context->pix_fmt = AV_PIX_FMT_YUVJ420P;
        return 0;
    }

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
        if (!strcmp(params->ecodec, "libx265")) {
            /* AV_PIX_FMT_YUV422P12LE: 24bpp, (1 Cr & Cb sample per 2x1 Y samples), little-endian */
            //encoder_codec_context->pix_fmt = AV_PIX_FMT_YUV422P12LE;
            encoder_codec_context->pix_fmt = AV_PIX_FMT_YUV420P12LE;
            break;
        }

        /* x264 doesn't support 12 bitdepth pixel format */
    default:
        elv_err("Invalid bitdepth=%d, url=%s", params->bitdepth, params->url);
        return eav_param;
    }

    return 0;
}

static int
prepare_video_encoder(
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

    if (params->bypass_transcoding) {
        AVStream *in_stream = decoder_context->stream[index];
        AVStream *out_stream = encoder_context->stream[index];
        AVCodecParameters *in_codecpar = in_stream->codecpar;

        rc = avcodec_parameters_copy(out_stream->codecpar, in_codecpar);
        if (rc < 0) {
            elv_err("BYPASS failed to copy codec parameters, url=%s", params->url);
            return eav_codec_param;
        }

        /* Set output stream timebase when bypass encoding */
        if (params->video_time_base > 0)
            out_stream->time_base = (AVRational) {1, params->video_time_base};
        else
            out_stream->time_base = in_stream->time_base;
        out_stream->avg_frame_rate = decoder_context->format_context->streams[decoder_context->video_stream_index]->avg_frame_rate;
        out_stream->codecpar->codec_tag = 0;

        rc = set_encoder_options(encoder_context, decoder_context, params, decoder_context->video_stream_index,
            out_stream->time_base.den);
        if (rc < 0) {
            elv_err("Failed to set video encoder options with bypass, url=%s", params->url);
            return rc;
        }
        return 0;
    }

    encoder_context->codec_context[index] = avcodec_alloc_context3(encoder_context->codec[index]);
    if (!encoder_context->codec_context[index]) {
        elv_dbg("could not allocated memory for codec context");
        return eav_codec_context;
    }

    AVCodecContext *encoder_codec_context = encoder_context->codec_context[index];

    /* Set encoder parameters (directly in the coder context priv_data dictionary) */

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

    /* Set codec context parameters */
    encoder_codec_context->height = params->enc_height != -1 ? params->enc_height : decoder_context->codec_context[index]->height;
    encoder_codec_context->width = params->enc_width != -1 ? params->enc_width : decoder_context->codec_context[index]->width;

    /* If the rotation param is set to 90 or 270 degree then change width and hight */
    if (params->rotate == 90 || params->rotate == 270) {
        encoder_codec_context->height = params->enc_width != -1 ? params->enc_width : decoder_context->codec_context[index]->width;
        encoder_codec_context->width = params->enc_height != -1 ? params->enc_height : decoder_context->codec_context[index]->height;
    }
    if (params->video_time_base > 0)
        encoder_codec_context->time_base = (AVRational) {1, params->video_time_base};
    else
        encoder_codec_context->time_base = decoder_context->codec_context[index]->time_base;
    encoder_codec_context->sample_aspect_ratio = decoder_context->codec_context[index]->sample_aspect_ratio;
    if (params->video_bitrate > 0)
        encoder_codec_context->bit_rate = params->video_bitrate;
    if (params->rc_buffer_size > 0)
        encoder_codec_context->rc_buffer_size = params->rc_buffer_size;
    if (params->rc_max_rate > 0)
        encoder_codec_context->rc_max_rate = params->rc_max_rate;
    encoder_codec_context->framerate = decoder_context->codec_context[index]->framerate;

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
    if ((rc = set_pixel_fmt(encoder_codec_context, params)) != eav_success)
        return rc;

    if (!strcmp(params->ecodec, "h264_nvenc"))
        /* Set NVIDIA specific params if the encoder is NVIDIA */
        set_nvidia_params(encoder_context, decoder_context, params);
    else if (!strcmp(params->ecodec, "libx265"))
        /* Set H265 specific params (profile and level) */
        set_h265_params(encoder_context, decoder_context, params);
    else if (!strcmp(params->ecodec, "h264_ni_enc"))
        /* Set netint H264 codensity params */
        set_netint_h264_params(encoder_context, decoder_context, params);
    else if (!strcmp(params->ecodec, "h265_ni_enc"))
        /* Set netint H265 codensity params */
        set_netint_h265_params(encoder_context, decoder_context, params);
    else
        /* Set H264 specific params (profile and level) */
        set_h264_params(encoder_context, decoder_context, params);

    elv_log("Output pixel_format=%s, profile=%d, level=%d",
        av_get_pix_fmt_name(encoder_codec_context->pix_fmt),
        encoder_codec_context->profile,
        encoder_codec_context->level);

    /* Set encoder options after setting all codec context parameters */
    rc = set_encoder_options(encoder_context, decoder_context, params, decoder_context->video_stream_index,
        encoder_codec_context->time_base.den);
    if (rc < 0) {
        elv_err("Failed to set video encoder options, url=%s", params->url);
        return rc;
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

    encoder_context->stream[index]->time_base = encoder_codec_context->time_base;
    encoder_context->stream[index]->avg_frame_rate = decoder_context->stream[decoder_context->video_stream_index]->avg_frame_rate;

    return 0;
}

static int
is_valid_aac_sample_rate(
    int sample_rate)
{
    int valid_sample_rates[] = {8000, 12000, 16000, 22050, 24000, 32000, 44100, 48000, 88200, 96000};

    for (int i=0; i<sizeof(valid_sample_rates)/sizeof(int); i++) {
        if (sample_rate == valid_sample_rates[i])
            return 1;
    }

    return 0;
}

static int
prepare_audio_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int n_audio = encoder_context->n_audio_output;
    char *ecodec;
    AVFormatContext *format_context;
    int rc;

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

        format_context->io_open = elv_io_open;
        format_context->io_close = elv_io_close;

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
        else
            /* If the input stream is stereo the decoder_context->codec_context[index]->channel_layout is AV_CH_LAYOUT_STEREO */
            encoder_context->codec_context[output_stream_index]->channel_layout =
                get_channel_layout_for_encoder(decoder_context->codec_context[stream_index]->channel_layout);
        encoder_context->codec_context[output_stream_index]->channels = av_get_channel_layout_nb_channels(encoder_context->codec_context[output_stream_index]->channel_layout);

        const char *channel_name = avpipe_channel_name(
                                av_get_channel_layout_nb_channels(encoder_context->codec_context[output_stream_index]->channel_layout),
                                decoder_context->codec_context[stream_index]->channel_layout);

        /* If decoder channel layout is DOWNMIX and params->ecodec == "aac" and channel_layout is not set
         * then set the channel layout to STEREO. Preserve the channel layout otherwise.
         */
        if (decoder_context->codec_context[stream_index]->channel_layout == AV_CH_LAYOUT_STEREO_DOWNMIX &&
            !strcmp(ecodec, "aac") &&
            !params->channel_layout) {
            /* This encoder is prepared specifically for AAC, therefore set the channel layout to AV_CH_LAYOUT_STEREO */
            encoder_context->codec_context[output_stream_index]->channels = av_get_channel_layout_nb_channels(AV_CH_LAYOUT_STEREO);
            encoder_context->codec_context[output_stream_index]->channel_layout = AV_CH_LAYOUT_STEREO;    // AV_CH_LAYOUT_STEREO is av_get_default_channel_layout(encoder_context->codec_context[index]->channels)
        }

        int sample_rate = params->sample_rate;
        if (!strcmp(ecodec, "aac") &&
            !is_valid_aac_sample_rate(encoder_context->codec_context[output_stream_index]->sample_rate) &&
            sample_rate <= 0)
            sample_rate = DEFAULT_ACC_SAMPLE_RATE;

        /*
         *  If sample_rate is set and
         *      - encoder is not "aac" or
         *      - if encoder is "aac" and encoder sample_rate is not valid and transcoding is pan/merge/join
         *  then
         *      - set encoder sample_rate to the specified sample_rate.
         */
        if (sample_rate > 0 &&
            (strcmp(ecodec, "aac") || !is_valid_aac_sample_rate(encoder_context->codec_context[output_stream_index]->sample_rate))) {
            /*
             * Audio resampling, which is active for aac encoder, needs more work to adjust sampling properly
             * when input sample rate is different from output sample rate. (--RM)
             */
            encoder_context->codec_context[output_stream_index]->sample_rate = sample_rate;
    
            /* Update timebase for the new sample rate */
            encoder_context->codec_context[output_stream_index]->time_base = (AVRational){1, sample_rate};
            encoder_context->stream[output_stream_index]->time_base = (AVRational){1, sample_rate};
        }

        elv_dbg("ENCODER channels=%d, channel_layout=%d (%s), sample_fmt=%s, sample_rate=%d",
            encoder_context->codec_context[output_stream_index]->channels,
            encoder_context->codec_context[output_stream_index]->channel_layout,
            avpipe_channel_layout_name(encoder_context->codec_context[output_stream_index]->channel_layout),
            av_get_sample_fmt_name(encoder_context->codec_context[output_stream_index]->sample_fmt),
            encoder_context->codec_context[output_stream_index]->sample_rate);

        encoder_context->codec_context[output_stream_index]->bit_rate = params->audio_bitrate;

        /* Allow the use of the experimental AAC encoder. */
        encoder_context->codec_context[output_stream_index]->strict_std_compliance = FF_COMPLIANCE_EXPERIMENTAL;

        rc = set_encoder_options(encoder_context, decoder_context, params, decoder_context->audio_stream_index[i],
            encoder_context->stream[output_stream_index]->time_base.den);
        if (rc < 0) {
            elv_err("Failed to set audio encoder options, url=%s", params->url);
            return rc;
        }

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

        elv_dbg("encoder audio stream index=%d, bitrate=%d, sample_fmts=%s, timebase=%d, output frame_size=%d, sample_rate=%d, channel_layout=%s",
            index, encoder_context->codec_context[output_stream_index]->bit_rate,
            av_get_sample_fmt_name(encoder_context->codec_context[output_stream_index]->sample_fmt),
            encoder_context->codec_context[output_stream_index]->time_base.den, encoder_context->codec_context[output_stream_index]->frame_size,
            encoder_context->codec_context[output_stream_index]->sample_rate,
    	channel_name);

        if (avcodec_parameters_from_context(
            encoder_context->stream[output_stream_index]->codecpar,
            encoder_context->codec_context[output_stream_index]) < 0) {
            elv_err("Failed to copy encoder parameters to output stream, url=%s", params->url);
            return eav_codec_param;

        }

#ifdef USE_RESAMPLE_AAC
        if (!strcmp(ecodec, "aac") &&
            params->xc_type & xc_audio &&
            params->xc_type != xc_audio_merge &&
            params->xc_type != xc_audio_join &&
            params->xc_type != xc_audio_pan) {
            init_resampler(decoder_context->codec_context[stream_index], encoder_context->codec_context[output_stream_index],
                       &decoder_context->resampler_context);

            /* Create the FIFO buffer based on the specified output sample format. */
            if (!(decoder_context->fifo = av_audio_fifo_alloc(encoder_context->codec_context[output_stream_index]->sample_fmt,
                    encoder_context->codec_context[index]->channels, 1))) {
                elv_err("Failed to allocate audio FIFO, url=%s", params->url);
                return eav_mem_alloc;
            }
        }
#endif

        //encoder_context->audio_enc_stream_index[i] = stream_index; CLEAN
    }

    return 0;
}

static int
num_audio_output(
    coderctx_t *decoder_context,
    xcparams_t *params)
{
    int n_decoder_auido = decoder_context ? decoder_context->n_audio : 0;
    if (!params)
        return 0;

    if (params->xc_type == xc_audio_merge || params->xc_type == xc_audio_join || params->xc_type == xc_audio_pan)
        return 1;

    return params->n_audio > 0 ? params->n_audio : n_decoder_auido;
}

static int
prepare_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    avpipe_io_handler_t *out_handlers,
    ioctx_t *inctx,
    xcparams_t *params)
{
    out_tracker_t *out_tracker;
    char *filename = "";
    char *filename2 = "";
    char *format = params->format;
    int rc = 0;

    encoder_context->is_mpegts = decoder_context->is_mpegts;
    encoder_context->is_rtmp = decoder_context->is_rtmp;
    encoder_context->out_handlers = out_handlers;
    /*
     * TODO: passing "hls" format needs some development in FF to produce stream index for audio/video.
     * I will keep hls as before to go to dashenc.c
     */
    if (!strcmp(params->format, "hls"))
        format = "dash";
    else if (!strcmp(params->format, "mp4")) {
        filename = "mp4-stream.mp4";
        if (params->xc_type == xc_all)
            filename2 = "mp4-astream.mp4";
    } else if (!strcmp(params->format, "fmp4")) {
        filename = "fmp4-stream.mp4";
        /* fmp4 is actually mp4 format with a fragmented flag */
        format = "mp4";
        if (params->xc_type == xc_all)
            filename2 = "fmp4-astream.mp4";
    } else if (!strcmp(params->format, "segment")) {
        filename = "segment-%05d.mp4";
        if (params->xc_type == xc_all)
            filename2 = "segment-audio-%05d.mp4";
    } else if (!strcmp(params->format, "fmp4-segment")) {
        /* Fragmented mp4 segment */
        format = "segment";
        if (params->xc_type & xc_video)
            filename = "fsegment-video-%05d.mp4";
        if (params->xc_type & xc_audio)
            filename2 = "fsegment-audio-%05d.mp4";
    } else if (!strcmp(params->format, "image2")) {
        filename = "%d.jpeg";
    }

    /*
     * Allocate an AVFormatContext for output.
     * Setting 3th paramter to "dash" determines the output file format and avoids guessing
     * output file format using filename in ffmpeg library.
     */
    if (params->xc_type & xc_video) {
        avformat_alloc_output_context2(&encoder_context->format_context, NULL, format, filename);
        if (!encoder_context->format_context) {
            elv_dbg("could not allocate memory for output format");
            return eav_codec_context;
        }
    }
    if (params->xc_type & xc_audio) {
        encoder_context->n_audio_output = num_audio_output(decoder_context, params);
        for (int i=0; i<encoder_context->n_audio_output; i++) {
            if (!strcmp(params->format, "hls") || !strcmp(params->format, "dash")) {
                avformat_alloc_output_context2(&encoder_context->format_context2[i], NULL, format, filename2);
            } else {
                snprintf(encoder_context->filename2[i], MAX_AVFILENAME_LEN, "fsegment-audio%d-%s.mp4", i, "%05d");
                avformat_alloc_output_context2(&encoder_context->format_context2[i], NULL, format, encoder_context->filename2[i]);
            }
            if (!encoder_context->format_context2[i]) {
                elv_dbg("could not allocate memory for audio output format stream_index=%d", params->audio_index[i]);
                return eav_codec_context;
            }
        }
    }

    if (params->xc_type == xc_extract_images || params->xc_type == xc_extract_all_images) {
        // Tell the image2 muxer to expand the filename with PTS instead of sequential numbering
        av_opt_set(encoder_context->format_context->priv_data, "frame_pts", "true", 0);
    }

    // Encryption applies to both audio and video
    // PENDING (RM) Set the keys for audio if xc_type == xc_all
    switch (params->crypt_scheme) {
    case crypt_aes128:
        if (params->xc_type & xc_video) {
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
        if (params->xc_type & xc_audio) {
            for (int i=0; i<encoder_context->n_audio_output; i++) {
                av_opt_set(encoder_context->format_context2[i]->priv_data, "hls_enc", "1", 0);
                if (params->crypt_iv != NULL)
                    av_opt_set(encoder_context->format_context2[i]->priv_data, "hls_enc_iv",
                        params->crypt_iv, 0);
                if (params->crypt_key != NULL)
                    av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "hls_enc_key", params->crypt_key, 0);
                if (params->crypt_key_url != NULL)
                    av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "hls_enc_key_url", params->crypt_key_url, 0);
            }
        }
        break;
    case crypt_cenc:
        /* PENDING (RM) after debugging cbcs we can remove old encryption scheme names. */
        if (params->xc_type & xc_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                      "encryption_scheme", "cenc", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-ctr", 0);
        }
        if (params->xc_type & xc_audio) {
            for (int i=0; i<encoder_context->n_audio_output; i++) {
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                      "encryption_scheme", "cenc", 0);
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "encryption_scheme", "cenc-aes-ctr", 0);
            }
        }
        break;
    case crypt_cbc1:
        if (params->xc_type & xc_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cbc1", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-cbc", 0);
        }
        if (params->xc_type & xc_audio) {
            for (int i=0; i<encoder_context->n_audio_output; i++) {
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "encryption_scheme", "cbc1", 0);
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "encryption_scheme", "cenc-aes-cbc", 0);
            }
        }
        break;
    case crypt_cens:
        if (params->xc_type & xc_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cens", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-ctr-pattern", 0);
        }
        if (params->xc_type & xc_audio) {
            for (int i=0; i<encoder_context->n_audio_output; i++) {
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "encryption_scheme", "cens", 0);
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "encryption_scheme", "cenc-aes-ctr-pattern", 0);
            }
        }
        break;
    case crypt_cbcs:
        if (params->xc_type & xc_video) {
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cbcs", 0);
            av_opt_set(encoder_context->format_context->priv_data,
                       "encryption_scheme", "cenc-aes-cbc-pattern", 0);
            av_opt_set(encoder_context->format_context->priv_data, "encryption_iv",
                params->crypt_iv, 0);
            av_opt_set(encoder_context->format_context->priv_data, "hls_enc_iv",        /* To remove */
                params->crypt_iv, 0);
        }
        if (params->xc_type & xc_audio) {
            for (int i=0; i<encoder_context->n_audio_output; i++) {
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "encryption_scheme", "cbcs", 0);
                av_opt_set(encoder_context->format_context2[i]->priv_data,
                       "encryption_scheme", "cenc-aes-cbc-pattern", 0);
                av_opt_set(encoder_context->format_context2[i]->priv_data, "encryption_iv",
                        params->crypt_iv, 0);
                av_opt_set(encoder_context->format_context2[i]->priv_data, "hls_enc_iv",        /* To remove */
                        params->crypt_iv, 0);
            }
        }
        break;
    case crypt_none:
        break;
    default:
        elv_err("Unimplemented crypt scheme: %d, url=%s", params->crypt_scheme, params->url);
    }

    switch (params->crypt_scheme) {
    case crypt_cenc:
    case crypt_cbc1:
    case crypt_cens:
    case crypt_cbcs:
        if (params->xc_type & xc_video) {
            av_opt_set(encoder_context->format_context->priv_data, "encryption_kid",
                       params->crypt_kid, 0);
            av_opt_set(encoder_context->format_context->priv_data, "encryption_key",
                       params->crypt_key, 0);
        }
        if (params->xc_type & xc_audio) {
            for (int i=0; i<encoder_context->n_audio_output; i++) {
                av_opt_set(encoder_context->format_context2[i]->priv_data, "encryption_kid",
                       params->crypt_kid, 0);
                av_opt_set(encoder_context->format_context2[i]->priv_data, "encryption_key",
                       params->crypt_key, 0);
            }
        }
    default:
        break;
    }

    if (params->xc_type & xc_video) {
        if ((rc = prepare_video_encoder(encoder_context, decoder_context, params)) != eav_success) {
            elv_err("Failure in preparing video encoder, rc=%d, url=%s", rc, params->url);
            return rc;
        }
    }

    if (params->xc_type & xc_audio) {
        //for (int i=0; i<MAX_STREAMS; i++) CLEAN
        //    encoder_context->audio_enc_stream_index[i] = -1;
        if ((rc = prepare_audio_encoder(encoder_context, decoder_context, params)) != eav_success) {
            elv_err("Failure in preparing audio encoder, rc=%d, url=%s", rc, params->url);
            return rc;
        }
    }

    /*
     * Allocate an array of MAX_STREAMS out_handler_t: one for video and one for each audio output stream.
     * Needs to allocate up to number of streams when transcoding multiple streams at the same time.
     */
    if (params->xc_type & xc_video) {
        out_tracker = (out_tracker_t *) calloc(MAX_STREAMS, sizeof(out_tracker_t));
        out_tracker[0].out_handlers = out_handlers;
        out_tracker[0].inctx = inctx;
        out_tracker[0].video_stream_index = decoder_context->video_stream_index;
        out_tracker[0].audio_stream_index = decoder_context->audio_stream_index[0];
        out_tracker[0].seg_index = atoi(params->start_segment_str);
        out_tracker[0].encoder_ctx = encoder_context;
        out_tracker[0].xc_type = xc_video;
        encoder_context->format_context->avpipe_opaque = out_tracker;
    }

    if (params->xc_type & xc_audio) {
        for (int j=0; j<encoder_context->n_audio_output; j++) {
            out_tracker = (out_tracker_t *) calloc(MAX_STREAMS, sizeof(out_tracker_t));
            for (int i=0; i<encoder_context->n_audio_output; i++) {
                out_tracker[i].out_handlers = out_handlers;
                out_tracker[i].inctx = inctx;
                out_tracker[i].video_stream_index = decoder_context->video_stream_index;
                out_tracker[i].audio_stream_index = decoder_context->audio_stream_index[i];
                out_tracker[i].seg_index = atoi(params->start_segment_str);
                out_tracker[i].encoder_ctx = encoder_context;
                out_tracker[i].xc_type = xc_audio;
            }
            encoder_context->format_context2[j]->avpipe_opaque = out_tracker;
        }
    }

    dump_encoder(inctx->url, encoder_context->format_context, params);
    dump_codec_context(encoder_context->codec_context[encoder_context->video_stream_index]);
    for (int i=0; i<encoder_context->n_audio_output; i++) {
        dump_encoder(inctx->url, encoder_context->format_context2[i], params);
    }
    dump_codec_context(encoder_context->codec_context[encoder_context->audio_stream_index[0]]);

    return 0;
}

static void
set_idr_frame_key_flag(
    AVFrame *frame,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params,
    int debug_frame_level)
{
    if (!frame)
        return;

    /* No need to set IDR key frame if the xc_type is not xc_video. */
    if ((params->xc_type & xc_video) == 0)
        return;

#if 1
    /*
     * If format is "dash" or "hls" then don't clear the flag, because dash/hls uses pict_type to determine end of segment.
     * The reset of the formats would be good to clear before encoding (see doc/examples/transcoding.c).
     */
    if (strcmp(params->format, "dash") && strcmp(params->format, "hls"))
#else
    /*
     * If decoder is prores or jpeg2000, then clear pict_type key frame flag and let the encoder to decide for that.
     */
    if (decoder_context->codec_parameters[decoder_context->video_stream_index]->codec_id == 147 ||
        decoder_context->codec_parameters[decoder_context->video_stream_index]->codec_id == 88)
#endif
        frame->pict_type = AV_PICTURE_TYPE_NONE;

    /*
     * Set key frame in the beginning of every abr segment.
     */
    if (!strcmp(params->format, "dash") || !strcmp(params->format, "hls")) {
        if (frame->pts >= encoder_context->last_key_frame + params->video_seg_duration_ts) {
            int64_t diff = frame->pts - (encoder_context->last_key_frame + params->video_seg_duration_ts);
            int missing_frames = 0;
            /* We can have some missing_frames only when transcoding UDP MPEG-TS */
            if (encoder_context->is_mpegts && encoder_context->calculated_frame_duration > 0)
                missing_frames = diff / encoder_context->calculated_frame_duration;
            if (debug_frame_level) {
                elv_dbg("FRAME SET KEY flag, seg_duration_ts=%d pts=%"PRId64", missing_frames=%d, last_key_frame_pts=%"PRId64,
                    params->video_seg_duration_ts, frame->pts, missing_frames, encoder_context->last_key_frame);
            }
            frame->pict_type = AV_PICTURE_TYPE_I;
            encoder_context->last_key_frame = frame->pts - missing_frames * encoder_context->calculated_frame_duration;
            encoder_context->forced_keyint_countdown = params->force_keyint - missing_frames;
        }
    }

    if (params->force_keyint > 0) {
        if (encoder_context->forced_keyint_countdown <= 0) {
            if (debug_frame_level) {
                elv_dbg("FRAME SET KEY flag, forced_keyint=%d pts=%"PRId64", forced_keyint_countdown=%d",
                    params->force_keyint, frame->pts, encoder_context->forced_keyint_countdown);
            }
            if (encoder_context->forced_keyint_countdown < 0)
                elv_log("force_keyint_countdown=%d", encoder_context->forced_keyint_countdown);
            frame->pict_type = AV_PICTURE_TYPE_I;
            encoder_context->last_key_frame = frame->pts;
            encoder_context->forced_keyint_countdown += params->force_keyint;
        }
        encoder_context->forced_keyint_countdown --;
    }
}

static int
is_frame_extraction_done(coderctx_t *encoder_context,
    xcparams_t *p)
{
    if (p->xc_type != xc_extract_images && p->xc_type != xc_extract_all_images)
        return 1;

    if (p->extract_images_sz > 0) {
        for (int i = 0; i < p->extract_images_sz; i++) {
            const int64_t wanted = p->extract_images_ts[i];
            if (wanted > encoder_context->video_last_pts_sent_encode) {
                return 0;
            }
        }
    } else {
        // TBD - It's a little harder to check when extracting intervals, and
        // there is no code that uses this right now
        return 0;
    }
    return 1;
}

/*
 * should_extract_frame checks when frames should be encoded when
 * xc_extract_images is set. Either extracts given a list of of exact pts
 * extract_images_ts, or at an interval extract_image_interval_ts.
 */
static int
should_extract_frame(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *p,
    AVFrame *frame)
{
    if (p->xc_type == xc_extract_all_images)
        return 1;

    if (p->xc_type != xc_extract_images)
        return 0;

    if (p->extract_images_sz > 0) {
        /* Extract specified frames */
        for (int i = 0; i < p->extract_images_sz; i++) {
            const int64_t wanted = p->extract_images_ts[i];
            // Time is right && We didn't already extract for this time
            if (frame->pts >= wanted && wanted > encoder_context->video_last_pts_sent_encode) {
                return 1;
            }
        }
    } else {
        /* Wait for the specified interval between encoding images/frames */
        int64_t interval = p->extract_image_interval_ts;
        if (interval < 0) {
            interval = DEFAULT_FRAME_INTERVAL_S *
                decoder_context->stream[decoder_context->video_stream_index]->time_base.den;
        }
        const int64_t time_past = frame->pts - encoder_context->video_last_pts_sent_encode;
        if (time_past >= interval || encoder_context->first_encoding_video_pts == -1) {
            return 1;
        }
    }
    return 0;
}

static int
should_skip_encoding(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    int stream_index,
    xcparams_t *p,
    AVFrame *frame)
{
    if (!frame ||
        p->xc_type == xc_audio_join ||
        p->xc_type == xc_audio_merge)
        return 0;

    int64_t frame_in_pts_offset;

    /*
     * If the input frame's PTS and DTS are not set, don't encode the frame.
     * This situation seems to happen sometimes with mpeg-ts streams.
     */
    if (frame->pts == AV_NOPTS_VALUE && frame->pkt_dts == AV_NOPTS_VALUE) {
        char *url = "";
        if (decoder_context->inctx && decoder_context->inctx->url)
            url = decoder_context->inctx->url;
        elv_warn("ENCODE SKIP invalid frame, stream_index=%d, url=%s, video_last_pts_read=%"PRId64", audio_last_pts_read=%"PRId64,
            stream_index, url,
            encoder_context->video_last_pts_read, encoder_context->audio_last_pts_read[stream_index]);
        return 1;
    }

    if (selected_decoded_audio(decoder_context, stream_index) >= 0)
        frame_in_pts_offset = frame->pts - decoder_context->audio_input_start_pts[stream_index];
    else
        frame_in_pts_offset = frame->pts - decoder_context->video_input_start_pts;

    /* Drop frames before the desired 'start_time'
     * If the format is dash or hls, we skip the frames in skip_until_start_time_pts()
     * without decoding the frame.
     */
    if (p->skip_decoding) {
        if (p->start_time_ts > 0 &&
            frame_in_pts_offset < p->start_time_ts &&
            strcmp(p->format, "dash") &&
            strcmp(p->format, "hls")) {
            elv_dbg("ENCODE SKIP frame early pts=%" PRId64 ", frame_in_pts_offset=%" PRId64 ", start_time_ts=%" PRId64,
                frame->pts, frame_in_pts_offset, p->start_time_ts);
            return 1;
        }
    } else if (p->start_time_ts > 0 && frame_in_pts_offset < p->start_time_ts) {
        elv_dbg("ENCODE SKIP frame early pts=%" PRId64 ", frame_in_pts_offset=%" PRId64 ", start_time_ts=%" PRId64,
            frame->pts, frame_in_pts_offset, p->start_time_ts);
        return 1;
    }

    /* To allow for packet reordering frames can come with pts past the desired duration */
    if (p->duration_ts > 0) {
        const int64_t max_valid_ts = p->start_time_ts + p->duration_ts;
        if (frame_in_pts_offset >= max_valid_ts) {
            elv_dbg("ENCODE SKIP frame late pts=%" PRId64 ", frame_in_pts_offset=%" PRId64 ", max_valid_ts=%" PRId64,
                frame->pts, frame_in_pts_offset, max_valid_ts);
            return 1;
        }
    }

    if (p->xc_type == xc_extract_images || p->xc_type == xc_extract_all_images) {
        return !should_extract_frame(decoder_context, encoder_context, p, frame);
    }

    return 0;
}

/*
 * encode_frame() encodes the frame and writes it to the output.
 * If the incoming stream is a mpeg-ts or a rtmp stream, encode_frame() adjusts the
 * frame pts and dts before sending the frame to the encoder.
 * It returns eav_success if encoding is successful and returns appropriate error
 * if error happens.
 */
static int
encode_frame(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVFrame *frame,
    int stream_index,
    xcparams_t *params,
    int debug_frame_level)
{
    int ret;
    int index = stream_index; 
    int rc = eav_success;
    AVFormatContext *format_context = encoder_context->format_context;
    AVCodecContext *codec_context = encoder_context->codec_context[stream_index];
    out_tracker_t *out_tracker;
    avpipe_io_handler_t *out_handlers;
    ioctx_t *outctx;

    if (params->xc_type == xc_audio_merge ||
        params->xc_type == xc_audio_join ||
        params->xc_type == xc_audio_pan) {
        index = 0;
    }
    codec_context = encoder_context->codec_context[index];

    int i = -1;
    if ((i = selected_decoded_audio(decoder_context, stream_index)) >= 0) {
        if (params->xc_type == xc_audio_merge ||
            params->xc_type == xc_audio_join ||
            params->xc_type == xc_audio_pan) {
            i = 0;
        }
        format_context = encoder_context->format_context2[i];
    }

    int skip = should_skip_encoding(decoder_context, encoder_context, stream_index, params, frame);
    if (skip)
        return eav_success;

    AVPacket *output_packet = av_packet_alloc();
    if (!output_packet) {
        elv_dbg("could not allocate memory for output packet");
        return eav_mem_alloc;
    }

    // Prepare packet before encoding - adjust PTS and IDR frame signaling
    if (frame) {

        const char *st = stream_type_str(decoder_context, stream_index);

        // Adjust PTS if input stream starts at an arbitrary value (i.e mostly for MPEG-TS/RTMP)
        if ( is_protocol(decoder_context) && (!strcmp(params->format, "fmp4-segment"))) {
            if (stream_index == decoder_context->video_stream_index) {
                if (encoder_context->first_encoding_video_pts == -1) {
                    /* Remember the first video PTS to use as an offset later */
                    encoder_context->first_encoding_video_pts = frame->pts;
                    elv_log("PTS first_encoding_video_pts=%"PRId64" dec=%"PRId64" pktdts=%"PRId64" first_read_packet_pts=%"PRId64" stream=%d:%s",
                        encoder_context->first_encoding_video_pts,
                        decoder_context->first_decoding_video_pts,
                        frame->pkt_dts,
                        encoder_context->first_read_packet_pts[stream_index], stream_index, st);
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
                if (encoder_context->first_encoding_audio_pts[stream_index] == AV_NOPTS_VALUE) {
                    /* Remember the first audio PTS to use as an offset later */
                    encoder_context->first_encoding_audio_pts[stream_index] = frame->pts;
                    elv_log("PTS stream_index=%d first_encoding_audio_pts=%"PRId64" dec=%"PRId64" first_read_packet_pts=%"PRId64" stream=%d:%s",
                        stream_index,
                        encoder_context->first_encoding_audio_pts[stream_index],
                        decoder_context->first_decoding_audio_pts[stream_index],
                        encoder_context->first_read_packet_pts[stream_index], stream_index, st);
                }

                // Adjust audio frame pts such that first frame sent to the encoder has PTS 0
                if (frame->pts != AV_NOPTS_VALUE) {
                    frame->pts -= encoder_context->first_encoding_audio_pts[stream_index];
                    frame->pkt_dts = frame->pts;
                }
                if (frame->best_effort_timestamp != AV_NOPTS_VALUE)
                    frame->best_effort_timestamp -= encoder_context->first_encoding_audio_pts[stream_index];
            }
#endif
        }

        // Signal if we need IDR frames
        if (params->xc_type & xc_video &&
            stream_index == decoder_context->video_stream_index) {
            set_idr_frame_key_flag(frame, decoder_context, encoder_context, params, debug_frame_level);
        }

        // Special case to extract the first frame image
        if (params->xc_type == xc_extract_images &&
            params->extract_images_sz == 0 &&
            encoder_context->first_encoding_video_pts == -1) {
            encoder_context->first_encoding_video_pts = frame->pts;
        }

        if (params->xc_type & xc_audio &&
            selected_decoded_audio(decoder_context, stream_index) >= 0)
            frame->pkt_duration = 0;

        dump_frame(selected_decoded_audio(decoder_context, stream_index) >= 0, stream_index,
            "TOENC ", codec_context->frame_number, frame, debug_frame_level);
    }

    // Send the frame to the encoder
    ret = avcodec_send_frame(codec_context, frame);
    if (ret < 0) {
        elv_err("Failed to send frame for encoding err=%d, url=%s", ret, params->url);
    }

    if (frame) {
        if (params->xc_type & xc_audio && selected_decoded_audio(decoder_context, stream_index) >= 0)
            encoder_context->audio_last_pts_sent_encode[stream_index] = frame->pts;
        else if (params->xc_type & xc_video && stream_index == decoder_context->video_stream_index)
            encoder_context->video_last_pts_sent_encode = frame->pts;
    }

    while (ret >= 0) {
        /* The packet must be initialized before receiving */
        av_init_packet(output_packet);
        output_packet->data = NULL;
        output_packet->size = 0;

        // Get the output packet from encoder
        ret = avcodec_receive_packet(codec_context, output_packet);

        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
            if (debug_frame_level)
                elv_dbg("encode_frame() EAGAIN in receiving packet, url=%s", params->url);
            break;
        } else if (ret < 0) {
            elv_err("Failure while receiving a packet from the encoder: %s, url=%s", av_err2str(ret), params->url);
            rc = eav_receive_packet;
            goto end_encode_frame;
        }

        /*
         * Sometimes the first audio frame comes out from encoder with a negarive pts (i.e replay rtmp with ffmpeg),
         * and after rescaling it becomes pretty big number which causes audio sync problem.
         * The only solution that I could come up for this was skipping this frame. (-RM)
         */
        if (selected_decoded_audio(decoder_context, stream_index) >= 0 && output_packet->pts < 0) {
            elv_log("Skipping encoded packet with negative pts %"PRId64, output_packet->pts);
            goto end_encode_frame;
        }

        /*
         * Set output_packet->stream_index to zero if output format only has one stream.
         * Preserve the stream index if output_packet->stream_index fits in output format.
         * Otherwise return error.
         */
        if (format_context->nb_streams == 1) {
            output_packet->stream_index = 0;
        } else if (output_packet->stream_index >= format_context->nb_streams) {
            elv_err("Output packet stream_index=%d is more than the number of streams=%d in output format, url=%s",
                output_packet->stream_index, format_context->nb_streams, params->url);
            rc = eav_stream_index;
            goto end_encode_frame;
        }

        /*
         * Set packet duration manually if not set by the encoder.
         * The logic is borrowed from dashenc.c dash_write_packet.
         * The main problem not having a packet duration is the calculation of the track length which
         * always ends up one packet short and then all rate and duration calculcations are slightly skewed.
         * See get_cluster_duration()
         */
        if (params->xc_type == xc_video)
            assert(output_packet->duration == 0); /* Only to notice if this ever gets set */
        if (selected_decoded_audio(decoder_context, stream_index) >= 0 && params->xc_type == xc_all) {
            if (!output_packet->duration && encoder_context->audio_last_dts[stream_index] != AV_NOPTS_VALUE)
                output_packet->duration = output_packet->dts - encoder_context->audio_last_dts[stream_index];
            encoder_context->audio_last_dts[stream_index] = output_packet->dts;
            encoder_context->audio_last_pts_encoded[stream_index] = output_packet->pts;
        } else {
            if (!output_packet->duration && encoder_context->video_last_dts != AV_NOPTS_VALUE)
                output_packet->duration = output_packet->dts - encoder_context->video_last_dts;
            encoder_context->video_last_dts = output_packet->dts;
            encoder_context->video_last_pts_encoded = output_packet->pts;
        }

        output_packet->pts += params->start_pts;
        output_packet->dts += params->start_pts;

        if ((decoder_context->is_mpegts || decoder_context->is_srt) &&
            encoder_context->video_encoder_prev_pts > 0 &&
            stream_index == decoder_context->video_stream_index &&
            encoder_context->calculated_frame_duration > 0 &&
            output_packet->pts != AV_NOPTS_VALUE &&
            output_packet->pts - encoder_context->video_encoder_prev_pts >=
                2*encoder_context->calculated_frame_duration &&
            params->xc_type != xc_extract_images &&
            params->xc_type != xc_extract_all_images) {
            elv_log("GAP detected, packet->pts=%"PRId64", video_encoder_prev_pts=%"PRId64", url=%s",
                output_packet->pts, encoder_context->video_encoder_prev_pts, params->url);
            encoder_context->forced_keyint_countdown -=
                (output_packet->pts - encoder_context->video_encoder_prev_pts)/encoder_context->calculated_frame_duration - 1;
        }

        if (stream_index == decoder_context->video_stream_index &&
            output_packet->pts != AV_NOPTS_VALUE)
            encoder_context->video_encoder_prev_pts = output_packet->pts;

        /*
         * Rescale using the stream time_base (not the codec context):
         *   - if the stream is a video or
         *   - if it is audio then the decoding stream and encoding stream has the same codec id.
         */
        if ((stream_index == decoder_context->video_stream_index ||
            (selected_decoded_audio(decoder_context, stream_index) >= 0 &&
             params->ecodec2 != NULL &&
             !strcmp(avcodec_get_name(decoder_context->codec_parameters[stream_index]->codec_id), params->ecodec2))) &&
            (decoder_context->stream[stream_index]->time_base.den !=
            encoder_context->stream[index]->time_base.den ||
            decoder_context->stream[stream_index]->time_base.num !=
            encoder_context->stream[index]->time_base.num)) {
            av_packet_rescale_ts(output_packet,
                decoder_context->stream[stream_index]->time_base,
                encoder_context->stream[index]->time_base
            );
        }

        if (selected_decoded_audio(decoder_context, stream_index) >= 0) {
            /* Set the packet duration if it is not the first audio packet */
            if (encoder_context->audio_pts[stream_index] != AV_NOPTS_VALUE)
                output_packet->duration = output_packet->pts - encoder_context->audio_pts[stream_index];
            else
                output_packet->duration = 0;
            encoder_context->audio_pts[stream_index] = output_packet->pts;
            encoder_context->audio_frames_written[stream_index]++;
        } else {
            if (encoder_context->video_pts != AV_NOPTS_VALUE)
                output_packet->duration = output_packet->pts - encoder_context->video_pts;
            else
                output_packet->duration = 0;
            encoder_context->video_pts = output_packet->pts;
            encoder_context->video_frames_written++;
        }

        /* If params->video_frame_duration_ts > 0, then set
           output packet duration and pts/dts regardless of previous calculations */
        if (stream_index == decoder_context->video_stream_index &&
            params->video_frame_duration_ts > 0) {
            output_packet->pts = output_packet->dts = params->start_pts + (encoder_context->video_frames_written - 1) * params->video_frame_duration_ts;
            output_packet->duration = params->video_frame_duration_ts;
            if (debug_frame_level)
                elv_dbg("output_packet->pts=%"PRId64", frame_number=%d",
                    output_packet->pts, encoder_context->video_frames_written);
        }

        dump_packet(selected_decoded_audio(decoder_context, stream_index) >= 0,
            "OUT ", output_packet, debug_frame_level);

        if (output_packet->pts == AV_NOPTS_VALUE ||
            output_packet->dts == AV_NOPTS_VALUE ||
            output_packet->data == NULL) {
            char *url = "";
            if (decoder_context->inctx && decoder_context->inctx->url)
                url = decoder_context->inctx->url;
            elv_warn("INVALID %s PACKET url=%s pts=%"PRId64" dts=%"PRId64" duration=%"PRId64" pos=%"PRId64" size=%d stream_index=%d flags=%x data=%p\n",
                selected_decoded_audio(decoder_context, stream_index) >= 0? "AUDIO" : "VIDEO", url,
                output_packet->pts, output_packet->dts, output_packet->duration,
                output_packet->pos, output_packet->size, output_packet->stream_index,
                output_packet->flags, output_packet->data);
        }

        /*
         * Update the stats before writing the packet to avoid a crash.
         * The outctx might be freed in av_interleaved_write_frame()
         */
        out_tracker = (out_tracker_t *) format_context->avpipe_opaque;
        out_handlers = out_tracker->out_handlers;
        outctx = out_tracker->last_outctx;

        if (out_handlers->avpipe_stater && outctx) {
            if (stream_index == decoder_context->video_stream_index)
                outctx->total_frames_written = encoder_context->video_frames_written;
            else
                outctx->total_frames_written = encoder_context->audio_frames_written[stream_index];
            outctx->frames_written++;
            out_handlers->avpipe_stater(outctx, stream_index, out_stat_frame_written);
        }

        /* mux encoded frame */
        ret = av_interleaved_write_frame(format_context, output_packet);
        if (ret != 0) {
            elv_err("Error %d writing output packet index=%d into stream_index=%d: %s, url=%s",
                ret, output_packet->stream_index, stream_index, av_err2str(ret), params->url);
            rc = eav_write_frame;
            break;
        }
    }

end_encode_frame:
    av_packet_unref(output_packet);
    av_packet_free(&output_packet);
    return rc;
}

static int
do_bypass(
    int is_audio,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    xcparams_t *p,
    int debug_frame_level)
{
    av_packet_rescale_ts(packet,
        decoder_context->stream[packet->stream_index]->time_base,
        encoder_context->stream[packet->stream_index]->time_base);

    packet->pts += p->start_pts;
    packet->dts += p->start_pts;

    dump_packet(is_audio, "BYPASS ", packet, debug_frame_level);

    AVFormatContext *format_context;

    if (is_audio) {
        int i = selected_decoded_audio(decoder_context, packet->stream_index);
        format_context = encoder_context->format_context2[i];
    } else
        format_context = encoder_context->format_context;

    if (packet->pts == AV_NOPTS_VALUE ||
        packet->dts == AV_NOPTS_VALUE ||
        packet->data == NULL) {
        char *url = "";
        if (decoder_context->inctx && decoder_context->inctx->url)
            url = decoder_context->inctx->url;
        elv_warn("INVALID %s PACKET (BYPASS) url=%s pts=%"PRId64" dts=%"PRId64" duration=%"PRId64" pos=%"PRId64" size=%d stream_index=%d flags=%x data=%p\n",
            is_audio ? "AUDIO" : "VIDEO", url,
            packet->pts, packet->dts, packet->duration,
            packet->pos, packet->size, packet->stream_index,
            packet->flags, packet->data);
    } else {
        if (av_interleaved_write_frame(format_context, packet) < 0) {
            elv_err("Failure in copying bypass packet xc_type=%d, url=%s", p->xc_type, p->url);
            return eav_write_frame;
        }

        out_tracker_t *out_tracker = (out_tracker_t *) format_context->avpipe_opaque;
        avpipe_io_handler_t *out_handlers = out_tracker->out_handlers;
        ioctx_t *outctx = out_tracker->last_outctx;

        if (out_handlers->avpipe_stater && outctx) {
            if (is_audio) {
                if (outctx->type != avpipe_audio_init_stream)
                    encoder_context->audio_frames_written[packet->stream_index]++;
                encoder_context->audio_last_pts_sent_encode[packet->stream_index] = packet->pts;
                outctx->total_frames_written = encoder_context->audio_frames_written[packet->stream_index];
            } else {
                if (outctx->type != avpipe_video_init_stream)
                    encoder_context->video_frames_written++;
                encoder_context->video_last_pts_sent_encode = packet->pts;
                outctx->total_frames_written = encoder_context->video_frames_written;
            }
            outctx->frames_written++;
            out_handlers->avpipe_stater(outctx, packet->stream_index, out_stat_frame_written);
        }
    }

    return eav_success;
}

static avpipe_error_t
check_pts_wrapped(
    int64_t *last_input_pts,
    AVFrame *frame,
    int stream_index)
{
    if (!frame || !last_input_pts)
        return eav_success;

    /* If the stream was wrapped then issue an error */
    if (*last_input_pts && *last_input_pts - frame->pts > MAX_WRAP_PTS) {
        elv_warn("PTS WRAPPED stream_index=%d, last_input_pts=%"PRId64", frame->pts=%"PRId64, stream_index, *last_input_pts, frame->pts);
        return eav_pts_wrapped;
    }

    if (frame->pts > *last_input_pts)
        *last_input_pts = frame->pts;

    return eav_success;
}

static int
transcode_audio(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    AVFrame *frame,
    AVFrame *filt_frame,
    int stream_index,
    xcparams_t *params,
    int debug_frame_level)
{
    int ret;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int audio_enc_stream_index = stream_index;
    int response;


    if (params->xc_type == xc_audio_merge ||
        params->xc_type == xc_audio_join ||
        params->xc_type == xc_audio_pan)
        audio_enc_stream_index = 0;

    if (debug_frame_level)
        elv_dbg("DECODE stream_index=%d send_packet pts=%"PRId64" dts=%"PRId64
            " duration=%d, input frame_size=%d, output frame_size=%d, audio_output_pts=%"PRId64,
            stream_index, packet->pts, packet->dts, packet->duration, codec_context->frame_size,
            encoder_context->codec_context[audio_enc_stream_index]->frame_size, decoder_context->audio_output_pts);

    if (params->bypass_transcoding) {
        return do_bypass(1, decoder_context, encoder_context, packet, params, debug_frame_level);
    }

    // Send the packet to the decoder
    response = avcodec_send_packet(codec_context, packet);
    if (response < 0) {
        /*
         * AVERROR_INVALIDDATA means the frame is invalid (mostly because of bad header).
         * Ignore the error and continue.
         */
        elv_err("Failure while sending an audio packet to the decoder: err=%d, %s, url=%s",
            response, av_err2str(response), params->url);
        // Ignore the error and continue
        return eav_success;
    }

    while (response >= 0) {
        // Get decoded frame
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN) || response == AVERROR_EOF) {
            break;
        } else if (response < 0) {
            elv_err("Failure while receiving a frame from the decoder: %s, url=%s",
                av_err2str(response), params->url);
            return eav_receive_frame;
        }

        if (decoder_context->first_decoding_audio_pts[stream_index] == AV_NOPTS_VALUE) {
            decoder_context->first_decoding_audio_pts[stream_index] = frame->pts;
            avpipe_io_handler_t *in_handlers = decoder_context->in_handlers;
            decoder_context->inctx->decoding_start_pts = decoder_context->first_decoding_audio_pts[stream_index];
            elv_log("stream_index=%d first_decoding_audio_pts=%"PRId64,
                stream_index, decoder_context->first_decoding_audio_pts[stream_index]);
            if (in_handlers->avpipe_stater)
                in_handlers->avpipe_stater(decoder_context->inctx, stream_index, in_stat_decoding_audio_start_pts);
        }

        dump_frame(1, stream_index, "IN ", codec_context->frame_number, frame, debug_frame_level);

        ret = check_pts_wrapped(&decoder_context->audio_last_input_pts[stream_index], frame, stream_index);
        if (ret == eav_pts_wrapped) {
            av_frame_unref(frame);
            return ret;
        }

        decoder_context->audio_pts[stream_index] = packet->pts;

        /* push the decoded frame into the filtergraph */
        int i = selected_decoded_audio(decoder_context, stream_index);
        if (i >= 0) {
            if (av_buffersrc_add_frame_flags(decoder_context->audio_buffersrc_ctx[i], frame, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
                elv_err("Failure in feeding into audio filtergraph source %d, url=%s", i, params->url);
                break;
            }
        }

        /* pull filtered frames from the filtergraph */
        while (1) {
            /* For audio join, merge or pan there is only one buffer sink (0) */
            if (params->xc_type == xc_audio_join ||
                params->xc_type == xc_audio_merge ||
                params->xc_type == xc_audio_pan)
                i = 0;
            ret = av_buffersink_get_frame(decoder_context->audio_buffersink_ctx[i], filt_frame);
            if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
                //elv_dbg("av_buffersink_get_frame() ret=EAGAIN");
                break;
            }

            if (ret < 0) {
                elv_err("Failed to execute audio frame filter ret=%d, url=%s", ret, params->url);
                return eav_receive_filter_frame;
            }

            dump_frame(1, stream_index, "FILT ", codec_context->frame_number, filt_frame, debug_frame_level);
            ret = encode_frame(decoder_context, encoder_context, filt_frame, packet->stream_index, params, debug_frame_level);
            av_frame_unref(filt_frame);
            if (ret == eav_write_frame) {
                av_frame_unref(frame);
                return ret;
            }
        }

        av_frame_unref(frame);
    }
    return eav_success;
}

#ifdef USE_RESAMPLE_AAC
static int
transcode_audio_aac(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    AVPacket *packet,
    AVFrame *frame,
    int stream_index,
    xcparams_t *p,
    int debug_frame_level)
{
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    AVCodecContext *output_codec_context = encoder_context->codec_context[stream_index];
    SwrContext *resampler_context = decoder_context->resampler_context;
    int response;
    AVFrame *filt_frame;
    int ret;

    if (debug_frame_level)
        elv_dbg("DECODE stream_index=%d send_packet pts=%"PRId64" dts=%"PRId64
            " duration=%d, input frame_size=%d, output frame_size=%d, url=%s",
            stream_index, packet->pts, packet->dts,
            packet->duration, codec_context->frame_size,
            encoder_context->codec_context[stream_index]->frame_size, p->url);

    if (p->bypass_transcoding) {
        return do_bypass(1, decoder_context, encoder_context, packet, p, debug_frame_level);
    }

    response = avcodec_send_packet(codec_context, packet);
    if (response < 0) {
        /*
         * AVERROR_INVALIDDATA means the frame is invalid (mostly because of bad header).
         * Ignore the error and continue.
         */
        elv_err("Failure while sending an audio packet to the decoder: err=%d, %s, url=%s",
            response, av_err2str(response), p->url);
        // Ignore the error and continue
        return eav_success;
    }

    while (response >= 0) {
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN) || response == AVERROR_EOF) {
            break;
        } else if (response < 0) {
            elv_err("Failure while receiving a frame from the decoder: %s, url=%s",
                av_err2str(response), p->url);
            return eav_receive_frame;
        }

        if (decoder_context->first_decoding_audio_pts[stream_index] == AV_NOPTS_VALUE) {
            decoder_context->first_decoding_audio_pts[stream_index] = frame->pts;
            avpipe_io_handler_t *in_handlers = decoder_context->in_handlers;
            decoder_context->inctx->decoding_start_pts = decoder_context->first_decoding_audio_pts[stream_index];
            elv_log("stream_index=%d first_decoding_audio_pts=%"PRId64,
                stream_index, decoder_context->first_decoding_audio_pts);
            if (in_handlers->avpipe_stater)
                in_handlers->avpipe_stater(decoder_context->inctx, stream_index, in_stat_decoding_audio_start_pts);
        }

        dump_frame(1, stream_index, "IN ", codec_context->frame_number, frame, debug_frame_level);

        ret = check_pts_wrapped(&decoder_context->audio_last_input_pts[stream_index], frame, stream_index);
        if (ret == eav_pts_wrapped) {
            av_frame_unref(frame);
            return ret;
        }

        decoder_context->audio_pts = packet->pts;
        /* Temporary storage for the converted input samples. */
        uint8_t **converted_input_samples = NULL;
        int input_frame_size = codec_context->frame_size > 0 ? codec_context->frame_size : frame->nb_samples;

        if (init_converted_samples(&converted_input_samples, output_codec_context, input_frame_size)) {
            elv_err("Failed to allocate audio samples, url=%s", p->url);
            return eav_audio_sample;
        }

        if (convert_samples((const uint8_t**)frame->extended_data, converted_input_samples,
                            input_frame_size, resampler_context)) {
            elv_err("Failed to convert audio samples, url=%s", p->url);
            return eav_audio_sample;
        }

        /* Store the new samples in the FIFO buffer. */
        if (av_audio_fifo_write(decoder_context->fifo, (void **)converted_input_samples,
                input_frame_size) < input_frame_size) {
            elv_err("Failed to write input frame to fifo frame_size=%d, url=%s",
                input_frame_size, p->url);
            return eav_write_frame;
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
                elv_err("Failed to read input samples from fifo frame_size=%d, url=%s", output_frame_size, p->url);
                av_frame_unref(filt_frame);
                return eav_receive_frame;
            }

            int64_t d;
            d = output_frame_size;

            while (d > 0) {
                /* When using FIFO frames no longer have PTS */
                filt_frame->pkt_dts = filt_frame->pts = decoder_context->audio_output_pts;

                if (decoder_context->audio_duration < filt_frame->pts) {
                    decoder_context->audio_duration = filt_frame->pts;

                    int should_skip = 0;
                    int64_t frame_in_pts_offset = frame->pts - decoder_context->audio_input_start_pts[stream_index];
                    /* If frame PTS < start_time_ts then don't encode audio frame */
                    if (p->start_time_ts > 0 && frame_in_pts_offset < p->start_time_ts) {
                         elv_dbg("ENCODE SKIP audio frame early pts=%" PRId64
                            ", frame_in_pts_offset=%" PRId64 ", start_time_ts=%" PRId64,
                            filt_frame->pts, frame_in_pts_offset, p->start_time_ts);
                        should_skip = 1;
                    }

                    if (!should_skip) {
                        ret = encode_frame(decoder_context, encoder_context, filt_frame, stream_index, p, debug_frame_level);
                        if (ret == eav_write_frame) {
                            av_frame_unref(filt_frame);
                            av_frame_free(&filt_frame);
                            return ret;
                        }
                    }
                }
                else {
                    elv_log("ENCODE SKIP audio frame pts=%"PRId64", duration=%"PRId64,
                        filt_frame->pts, decoder_context->audio_duration);
                }

                decoder_context->audio_output_pts += d;
                d = 0;
            }

            av_frame_unref(filt_frame);
            av_frame_free(&filt_frame);
        }

        av_frame_unref(frame);
    }
    return eav_success;
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
    xcparams_t *p,
    int do_instrument,
    int debug_frame_level)
{
    int ret;
    struct timeval tv;
    u_int64_t since;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int response;

    if (debug_frame_level)
        elv_dbg("DECODE stream_index=%d send_packet pts=%"PRId64" dts=%"PRId64" duration=%d",
            stream_index, packet->pts, packet->dts, packet->duration);

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
        elv_err("Failure while sending a video packet to the decoder: %s (%d), url=%s",
            av_err2str(response), response, p->url);
        if (response == AVERROR_INVALIDDATA)
            /*
             * AVERROR_INVALIDDATA means the frame is invalid (mostly because of bad header).
             * To avoid premature termination jump over the bad frame and continue decoding.
             */
            return eav_success;
        return eav_send_packet;
    }

    while (response >= 0) {
        elv_get_time(&tv);

        /* read decoded frame from decoder */
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN) || response == AVERROR_EOF) {
            break;
        } else if (response < 0) {
            elv_err("Failure while receiving a frame from the decoder: %s, url=%s",
                av_err2str(response), p->url);
            return eav_receive_frame;
        }

        if (decoder_context->first_decoding_video_pts == AV_NOPTS_VALUE) {
            decoder_context->first_decoding_video_pts = frame->pts;
            avpipe_io_handler_t *in_handlers = decoder_context->in_handlers;
            decoder_context->inctx->decoding_start_pts = decoder_context->first_decoding_video_pts;
            elv_log("first_decoding_video_pts=%"PRId64" pktdts=%"PRId64,
                decoder_context->first_decoding_video_pts, frame->pkt_dts);
            if (in_handlers->avpipe_stater)
                in_handlers->avpipe_stater(decoder_context->inctx, stream_index, in_stat_decoding_video_start_pts);
        }

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

        dump_frame(0, stream_index, "IN ", codec_context->frame_number, frame, debug_frame_level);

        ret = check_pts_wrapped(&decoder_context->audio_last_input_pts[stream_index], frame, stream_index);
        if (ret == eav_pts_wrapped) {
            av_frame_unref(frame);
            return ret;
        }

        if (do_instrument) {
            elv_since(&tv, &since);
            elv_log("INSTRMNT avcodec_receive_frame time=%"PRId64, since);
        }

        decoder_context->video_pts = packet->pts;

        /* push the decoded frame into the filtergraph */
        elv_get_time(&tv);
        if (av_buffersrc_add_frame_flags(decoder_context->video_buffersrc_ctx, frame, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
            elv_err("Failure in feeding the filtergraph, url=%s", p->url);
            break;
        }

        if (do_instrument) {
            elv_since(&tv, &since);
            elv_log("INSTRMNT av_buffersrc_add_frame_flags time=%"PRId64", url=%s", since, p->url);
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
                elv_err("Failed to execute frame filter ret=%d, url=%s", ret, p->url);
                return eav_receive_filter_frame;
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

            dump_frame(0, stream_index, "FILT ", codec_context->frame_number, filt_frame, debug_frame_level);
            filt_frame->pkt_dts = filt_frame->pts;

            elv_get_time(&tv);
            if (decoder_context->video_duration < filt_frame->pts) {
                decoder_context->video_duration = filt_frame->pts;
                ret = encode_frame(decoder_context, encoder_context, filt_frame, stream_index, p, debug_frame_level);
                if (ret == eav_write_frame) {
                    av_frame_unref(filt_frame);
                    av_frame_unref(frame);
                    return ret;
                }
            } else {
                elv_log("ENCODE SKIP video frame pts=%"PRId64", duration=%"PRId64", url=%s",
                    filt_frame->pts, decoder_context->video_duration, p->url);
            }

            if (do_instrument) {
                elv_since(&tv, &since);
                elv_log("INSTRMNT encode_frame time=%"PRId64", url=%s", since, p->url);
            }

            av_frame_unref(filt_frame);
        }
        av_frame_unref(frame);
    }
    return eav_success;
}

void *
transcode_video_func(
    void *p)
{
    xctx_t *xctx = (xctx_t *) p;
    coderctx_t *decoder_context = &xctx->decoder_ctx;
    coderctx_t *encoder_context = &xctx->encoder_ctx;
    xcparams_t *params = xctx->params;
    xc_frame_t *xc_frame;
    int err = 0;

    AVFrame *frame = av_frame_alloc();
    AVFrame *filt_frame = av_frame_alloc();

    while (!xctx->stop || elv_channel_size(xctx->vc) > 0) {

        xc_frame = elv_channel_receive(xctx->vc);
        if (!xc_frame) {
            elv_dbg("trancode_video_func, there is no frame, url=%s", params->url);
            continue;
        }

        AVPacket *packet = xc_frame->packet;
        if (!packet) {
            elv_err("transcode_video_func, packet is NULL, url=%s", params->url);
            free(xc_frame);
            continue;
        }

        // Image extraction optimization is possible here: By skipping the
        // decoder unless the packet, 1) contains a keyframe, and 2) is not
        // within the specified interval after the last frame was extracted.
        // However, this flag might not be set reliably for all input video
        // formats/codecs).
        //
        // else if (!(packet->flags & AV_PKT_FLAG_KEY) ...) {
        //     continue;
        // }
        if (params->xc_type == xc_extract_images || params->xc_type == xc_extract_all_images) {
            if (is_frame_extraction_done(encoder_context, params)) {
                elv_dbg("all frames already extracted, url=%s", params->url);
                av_packet_free(&packet);
                free(xc_frame);
                break;
            }
        }

        dump_packet(0, "IN THREAD", packet, xctx->debug_frame_level);

        err = transcode_video(
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

void *
transcode_audio_func(
    void *p)
{
    xctx_t *xctx = (xctx_t *) p;
    coderctx_t *decoder_context = &xctx->decoder_ctx;
    coderctx_t *encoder_context = &xctx->encoder_ctx;
    xcparams_t *params = xctx->params;
    xc_frame_t *xc_frame;
    int err = 0;

    AVFrame *frame = av_frame_alloc();
    AVFrame *filt_frame = av_frame_alloc();

    while (!xctx->stop || elv_channel_size(xctx->ac) > 0) {

        xc_frame = elv_channel_receive(xctx->ac);
        if (!xc_frame) {
            elv_dbg("trancode_audio_func, there is no frame, url=%s", params->url);
            continue;
        }

        AVPacket *packet = xc_frame->packet;
        if (!packet) {
            elv_err("transcode_audio_func, packet is NULL, url=%s", params->url);
            free(xc_frame);
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
            params->xc_type != xc_audio_join &&
            params->xc_type != xc_audio_merge &&
            params->xc_type != xc_audio_pan) {
            err = transcode_audio_aac(
                decoder_context,
                encoder_context,
                packet,
                frame,
                packet->stream_index,
                params,
                xctx->debug_frame_level);
        } else {
            err = transcode_audio(
                decoder_context,
                encoder_context,
                packet,
                frame,
                filt_frame,
                packet->stream_index,
                params,
                xctx->debug_frame_level);
            av_frame_unref(filt_frame);
        }
#else
        err = transcode_audio(
            decoder_context,
            encoder_context,
            packet,
            frame,
            filt_frame,
            packet->stream_index,
            params,
            xctx->debug_frame_level);
        av_frame_unref(filt_frame);
#endif

        av_frame_unref(frame);
        av_packet_free(&packet);
        free(xc_frame);

        if (err != eav_success) {
            elv_err("Stop audio transcoding, err=%d, url=%s", err, params->url);
            break;
        }

    }

    av_frame_free(&frame);
    av_frame_free(&filt_frame);
    if (!xctx->err)
        xctx->err = err;
    
    elv_channel_close(xctx->ac, 0);
    elv_dbg("transcode_audio_func err=%d, xctx->err=%d, stop=%d", err, xctx->err, xctx->stop);

    return NULL;
}

static int
flush_decoder(
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    int stream_index,
    xcparams_t *p,
    int debug_frame_level)
{
    int ret;
    int i;
    AVFrame *frame, *filt_frame;
    AVFilterContext *buffersink_ctx = decoder_context->video_buffersink_ctx;
    AVFilterContext *buffersrc_ctx = decoder_context->video_buffersrc_ctx;
    AVCodecContext *codec_context = decoder_context->codec_context[stream_index];
    int response = 0;

    if (codec_context == NULL)
        return eav_success;

    response = avcodec_send_packet(codec_context, NULL);    /* Passing NULL means flush the decoder buffers */
    frame = av_frame_alloc();
    filt_frame = av_frame_alloc();

    if (!p->bypass_transcoding &&
        (i = selected_decoded_audio(decoder_context, stream_index)) >= 0) {
        buffersrc_ctx = decoder_context->audio_buffersrc_ctx[i];
        buffersink_ctx = decoder_context->audio_buffersink_ctx[i];
    }

    while (response >=0) {
        response = avcodec_receive_frame(codec_context, frame);
        if (response == AVERROR(EAGAIN)) {
            break;
        }

        if (response == AVERROR_EOF) {
            elv_log("GOT EOF url=%s, xc_type=%d, format=%s", p->url, p->xc_type, p->format);
            continue; // PENDING(SSS) why continue and not break?
        }

        dump_frame(selected_decoded_audio(decoder_context, stream_index) >= 0, stream_index,
            "IN FLUSH", codec_context->frame_number, frame, debug_frame_level);

        if (codec_context->codec_type == AVMEDIA_TYPE_VIDEO ||
            codec_context->codec_type == AVMEDIA_TYPE_AUDIO) {

            /* push the decoded frame into the filtergraph */
            if (av_buffersrc_add_frame_flags(buffersrc_ctx, frame, AV_BUFFERSRC_FLAG_KEEP_REF) < 0) {
                elv_err("Failure in feeding the filtergraph, url=%s", p->url);
                break;
            }

            /* pull filtered frames from the filtergraph */
            while (1) {
                ret = av_buffersink_get_frame(buffersink_ctx, filt_frame);
                if (ret == AVERROR(EAGAIN)) {
                    break;
                }

                if (ret == AVERROR_EOF) {
                    elv_log("GOT EOF buffersink url=%s, xc_type=%d, format=%s", p->url, p->xc_type, p->format);
                    break;
                }

                dump_frame(selected_decoded_audio(decoder_context, stream_index) >= 0, stream_index,
                    "FILT ", codec_context->frame_number, filt_frame, debug_frame_level);

                ret = encode_frame(decoder_context, encoder_context, filt_frame, stream_index, p, debug_frame_level);
                av_frame_unref(filt_frame);
                if (ret == eav_write_frame)
                    return ret;
            }
        }
        av_frame_unref(frame);
    }

    av_frame_free(&filt_frame);
    av_frame_free(&frame);
    return eav_success;
}

int
should_stop_decoding(
    AVPacket *input_packet,
    coderctx_t *decoder_context,
    coderctx_t *encoder_context,
    xcparams_t *params,
    int64_t audio_frames_read,
    int64_t video_frames_read,
    int *frames_read_past_duration,
    int frames_allowed_past_duration)
{
    int64_t input_packet_rel_pts = 0;
    int stream_index = input_packet->stream_index;

    if (decoder_context->cancelled)
        return 1;

    if (stream_index != decoder_context->video_stream_index &&
        selected_decoded_audio(decoder_context, stream_index) < 0)
        return 0;

    if (stream_index == decoder_context->video_stream_index &&
        (params->xc_type & xc_video)) {
        if (decoder_context->video_input_start_pts == AV_NOPTS_VALUE) {
            decoder_context->video_input_start_pts = input_packet->pts;
            elv_log("video_input_start_pts=%"PRId64" flags=%d dts=%"PRId64,
                decoder_context->video_input_start_pts, input_packet->flags, input_packet->dts);
        }

        input_packet_rel_pts = input_packet->pts - decoder_context->video_input_start_pts;
    } else if (selected_decoded_audio(decoder_context, stream_index) >= 0 &&
        params->xc_type & xc_audio) {
        if (decoder_context->audio_input_start_pts[stream_index] == AV_NOPTS_VALUE) {
            decoder_context->audio_input_start_pts[stream_index] = input_packet->pts;
            elv_log("stream_index=%d audio_input_start_pts=%"PRId64,
                stream_index, decoder_context->audio_input_start_pts[stream_index]);
        }

        input_packet_rel_pts = input_packet->pts - decoder_context->audio_input_start_pts[stream_index];
    }

    /* PENDING (RM) for some of the live feeds (like RTMP) we need to scale input_packet_rel_pts */
    if (params->duration_ts != -1 &&
        input_packet->pts != AV_NOPTS_VALUE &&
        input_packet_rel_pts >= params->start_time_ts + params->duration_ts) {

        (*frames_read_past_duration) ++;
        elv_dbg("DURATION OVER param start_time=%"PRId64" duration=%"PRId64" pkt pts=%"PRId64" rel_pts=%"PRId64" "
                "audio_frames_read=%"PRId64", video_frames_read=%"PRId64", past_duration=%d",
                params->start_time_ts, params->duration_ts, input_packet->pts, input_packet_rel_pts,
                audio_frames_read, video_frames_read, *frames_read_past_duration);

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
        if (selected_decoded_audio(decoder_context, stream_index) >= 0 &&
            params->xc_type & xc_audio)
            encoder_context->audio_last_pts_read[stream_index] = input_packet->pts;
        else if (stream_index == decoder_context->video_stream_index &&
            params->xc_type & xc_video)
            encoder_context->video_last_pts_read = input_packet->pts;
    }
    return 0;
}

static int
skip_until_start_time_pts(
    coderctx_t *decoder_context,
    AVPacket *input_packet,
    xcparams_t *params)
{
    /* If start_time_ts > 0 and it is a bypass skip here
     * Also if start_time_ts > 0 and skip_decoding is set then skip here
     */
    if (params->start_time_ts <= 0 || (!params->skip_decoding && !params->bypass_transcoding))
        return 0;

    /* If the format is not dash/hls then return.
     * For dash/hls format, we know the mezzanines are generated by avpipe
     * and there is no BFrames in mezzanines. Therefore, it is safe to skip
     * the frame without decoding the frame.
     */
    if (strcmp(params->format, "dash") && strcmp(params->format, "hls"))
        return 0;

    int64_t input_start_pts;
    if (params->xc_type == xc_video)
        input_start_pts = decoder_context->video_input_start_pts;
    else
        input_start_pts = decoder_context->audio_input_start_pts[input_packet->stream_index];

    const int64_t packet_in_pts_offset = input_packet->pts - input_start_pts;
    /* Drop frames before the desired 'start_time' */
    if (packet_in_pts_offset < params->start_time_ts) {
        elv_dbg("PREDECODE SKIP frame early stream_index=%d, pts=%" PRId64 ", start_time_ts=%" PRId64
            ", input_start_pts=%" PRId64 ", packet_in_pts_offset=%" PRId64,
            input_packet->stream_index,
            input_packet->pts, params->start_time_ts,
            input_start_pts, packet_in_pts_offset);
        return 1;
    }

    return 0;
}

static int
skip_for_sync(
    coderctx_t *decoder_context,
    AVPacket *input_packet,
    xcparams_t *params)
{
    if (params->sync_audio_to_stream_id < 0)
        return 0;

    /* No need to sync if:
     * - it is not mpegts and not rtmp and not srt
     * - or it is already synced
     * - or format is not fmp4-segment.
     */
    if (!is_protocol(decoder_context) ||
        decoder_context->is_av_synced ||
        strcmp(params->format, "fmp4-segment"))
        return 0;

    /* Check if the packet is video and it is a key frame */
    if (input_packet->stream_index == decoder_context->video_stream_index) {
        /* first_key_frame_pts points to first video key frame. */
        if (decoder_context->first_key_frame_pts == AV_NOPTS_VALUE &&
            input_packet->flags == AV_PKT_FLAG_KEY) {
            avpipe_io_handler_t *in_handlers = decoder_context->in_handlers;
            decoder_context->first_key_frame_pts = input_packet->pts;
            decoder_context->inctx->first_key_frame_pts = decoder_context->first_key_frame_pts;
            elv_log("PTS first_key_frame_pts=%"PRId64" sidx=%d flags=%d dts=%"PRId64,
                decoder_context->first_key_frame_pts, input_packet->stream_index, input_packet->flags, input_packet->dts);

            if (in_handlers->avpipe_stater)
                in_handlers->avpipe_stater(decoder_context->inctx, input_packet->stream_index, in_stat_first_keyframe_pts);

            dump_packet(0, "SYNC ", input_packet, 1);
            return 0;
        }
        if (decoder_context->first_key_frame_pts == AV_NOPTS_VALUE) {
            dump_packet(0, "SYNC SKIP ", input_packet, 1);
            return 1;
        }
        return 0;
    }

    /* We are processing the audio packets now.
     * Skip until the audio PTS has reached the first video key frame PTS
     * PENDING(SSS) - this is incorrect if audio PTS is muxed ahead of video
     */
    if (decoder_context->first_key_frame_pts == AV_NOPTS_VALUE ||
        input_packet->pts < decoder_context->first_key_frame_pts) {
        elv_log("PTS SYNC SKIP audio_pts=%"PRId64" first_key_frame_pts=%"PRId64,
            input_packet->pts, decoder_context->first_key_frame_pts);
        dump_packet(1, "SYNC SKIP ", input_packet, 1);
        return 1;
    }

    decoder_context->is_av_synced = 1;
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
    xcparams_t *params)
{
    *filter_str = NULL;

    // PENDING(SS) TEST DEINTERLACE
    *filter_str = strdup("bwdif=mode=send_frame:parity=auto:deint=all");
    return eav_success;

    if (params->rotate > 0) {
        switch (params->rotate) {
        case 90:
            *filter_str = strdup("transpose=1");                // 90 degree rotation
            return eav_success;
        case 180:
            *filter_str = strdup("transpose=1,transpose=1");    // 180 degree rotation
            return eav_success;
        case 270:
            *filter_str = strdup("transpose=2");                // 270 degree rotation
            return eav_success;
        default:
            elv_err("Invalid param rotate=%d", params->rotate);
            return eav_param;
        }
    }

    if ((params->watermark_text && *params->watermark_text != '\0') ||
            (params->watermark_timecode && *params->watermark_timecode != '\0')) {
        char local_filter_str[FILTER_STRING_SZ];
        int shadow_x = 0;
        int shadow_y = 0;
        int font_size = 0;
        const char* filterTemplate =
            "scale=%d:%d, drawtext=text='%s':fontcolor=%s:fontsize=%d:x=%s:y=%s:shadowx=%d:shadowy=%d:shadowcolor=%s:alpha=0.65";
        int ret = 0;

        /* Return an error if one of the watermark params is not set properly */
        if ((!params->watermark_font_color || *params->watermark_font_color == '\0') ||
            (!params->watermark_xloc || *params->watermark_xloc == '\0') ||
            (params->watermark_relative_sz > 1 || params->watermark_relative_sz < 0) ||
            (!params->watermark_yloc || *params->watermark_yloc == '\0') ||
            (params->watermark_shadow && (!params->watermark_shadow_color || *params->watermark_shadow_color == '\0'))) {
            elv_err("Watermark params are not set correctly. color=\"%s\", relative_size=\"%f\", xloc=\"%s\", yloc=\"%s\", shadow=%d, shadow_color=\"%s\", url=%s",
                params->watermark_font_color != NULL ? params->watermark_font_color : "",
                params->watermark_relative_sz,
                params->watermark_xloc != NULL ? params->watermark_xloc : "",
                params->watermark_yloc != NULL ? params->watermark_yloc : "",
                params->watermark_shadow,
                params->watermark_shadow_color != NULL ? params->watermark_shadow_color : "", params->url);
            return eav_filter_string_init;
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
                elv_err("Watermark timecode params are not set correctly, rate=%f, url=%s", params->watermark_timecode_rate, params->url);
                return eav_filter_string_init;
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
            local_filter_str, strlen(local_filter_str), params->watermark_xloc,
            params->watermark_yloc, params->watermark_relative_sz, ret);
        if (ret < 0) {
            return eav_filter_string_init;
        } else if (ret >= FILTER_STRING_SZ) {
            elv_dbg("Not enough memory for watermark filter");
            return eav_filter_string_init;
        }
        *filter_str = (char *) calloc(strlen(local_filter_str)+1, 1);
        strcpy(*filter_str, local_filter_str);
    } else if (params->watermark_overlay && params->watermark_overlay[0] != '\0') {
        char *filt_buf = NULL;
        int filt_buf_size;
        int filt_str_len;
        const char* filt_template =
            "[in] scale=%d:%d [in-1]; movie='%s', setpts=PTS [over]; [in-1] setpts=PTS [in-1a]; [in-1a][over]  overlay='%s:%s:alpha=0.1' [out]";

        /* Return an error if one of the watermark params is not set properly */
        if ((!params->watermark_xloc || *params->watermark_xloc == '\0') ||
            (params->watermark_overlay_type == unknown_image) ||
            (!params->watermark_yloc || *params->watermark_yloc == '\0')) {
            elv_err("Watermark overlay params are not set correctly. overlay_type=\"%d\", xloc=\"%s\", yloc=\"%s\", url=%s",
                params->watermark_overlay_type,
                params->watermark_xloc != NULL ? params->watermark_xloc : "",
                params->watermark_yloc != NULL ? params->watermark_yloc : "", params->url);
            return eav_filter_string_init;
        }

        filt_buf_size = get_overlay_filter_string(&filt_buf,
            params->watermark_overlay, params->watermark_overlay_len, params->watermark_overlay_type);
        if (filt_buf_size < 0)
            return eav_filter_string_init;

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
            return eav_filter_string_init;
        } else if (ret >= filt_str_len) {
            free(*filter_str);
            elv_dbg("Not enough memory for overlay watermark filter");
            return eav_filter_string_init;
        }
    } else {
        if (!encoder_context->codec_context[encoder_context->video_stream_index]) {
            elv_err("Failed to make filter string, invalid codec context (check params), url=%s", params->url);
            return eav_filter_string_init;
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
    xctx_t *xctx,
    int do_instrument)
{
    /* Set scale filter */
    char *filter_str = NULL;
    coderctx_t *decoder_context = &xctx->decoder_ctx;
    coderctx_t *encoder_context = &xctx->encoder_ctx;
    xcparams_t *params = xctx->params;
    int debug_frame_level = params->debug_frame_level;
    avpipe_io_handler_t *in_handlers = xctx->in_handlers;
    ioctx_t *inctx = xctx->inctx;
    int rc = 0;
    int av_read_frame_rc = 0;
    AVPacket *input_packet = NULL;

    if (!params->bypass_transcoding &&
        (params->xc_type & xc_video)) {
        if ((rc = get_filter_str(&filter_str, encoder_context, params)) != eav_success) {
            goto xc_done;
        }

        if ((rc = init_video_filters(filter_str, decoder_context, encoder_context, xctx->params)) != eav_success) {
            free(filter_str);
            elv_err("Failed to initialize video filter, url=%s", params->url);
            goto xc_done;
        }
        free(filter_str);
    }

    if (!params->bypass_transcoding &&
        (params->xc_type & xc_audio) &&
        params->xc_type != xc_audio_join &&
        params->xc_type != xc_audio_pan &&
        params->xc_type != xc_audio_merge &&
        (rc = init_audio_filters(decoder_context, encoder_context, xctx->params)) != eav_success) {
        elv_err("Failed to initialize audio filter, url=%s", params->url);
        goto xc_done;
    }

    if (!params->bypass_transcoding &&
        params->xc_type == xc_audio_pan &&
        (rc = init_audio_pan_filters(xctx->params->filter_descriptor, decoder_context, encoder_context)) != eav_success) {
        elv_err("Failed to initialize audio pan filter, url=%s", params->url);
        goto xc_done;
    }

    if (!params->bypass_transcoding &&
        params->xc_type == xc_audio_join &&
        (rc = init_audio_join_filters(decoder_context, encoder_context, xctx->params)) != eav_success) {
        elv_err("Failed to initialize audio join filter, url=%s", params->url);
        goto xc_done;
    }

    if (!params->bypass_transcoding &&
        params->xc_type == xc_audio_merge &&
        (rc = init_audio_merge_pan_filters(xctx->params->filter_descriptor, decoder_context, encoder_context)) != eav_success) {
        elv_err("Failed to initialize audio merge pan filter, url=%s", params->url);
        goto xc_done;
    }

    if ((params->xc_type & xc_video) &&
        avformat_write_header(encoder_context->format_context, NULL) != eav_success) {
        elv_err("Failed to write video output file header, url=%s", params->url);
        rc = eav_write_header;
        goto xc_done;
    }

    if (params->xc_type & xc_audio) {
        for (int i=0; i<encoder_context->n_audio_output; i++) {
            if (avformat_write_header(encoder_context->format_context2[i], NULL) != eav_success) {
                elv_err("Failed to write audio output file header, url=%s", params->url);
                rc = eav_write_header;
                goto xc_done;
            }
        }
    }

    int video_stream_index = decoder_context->video_stream_index;
    if (params->xc_type & xc_video) {
        if (encoder_context->format_context->streams[0]->avg_frame_rate.num != 0 &&
            decoder_context->stream[video_stream_index]->time_base.num != 0) {
            encoder_context->calculated_frame_duration =
                /* In very rare cases this might overflow, so type cast to 64bit int to avoid overflow */
                ((int64_t)decoder_context->stream[video_stream_index]->time_base.den * (int64_t)encoder_context->format_context->streams[0]->avg_frame_rate.den) /
                    ((int64_t)encoder_context->format_context->streams[0]->avg_frame_rate.num * (int64_t) decoder_context->stream[video_stream_index]->time_base.num);
        }
        elv_log("calculated_frame_duration=%d", encoder_context->calculated_frame_duration);
    }

    xctx->do_instrument = do_instrument;
    xctx->debug_frame_level = debug_frame_level;

#if INPUT_IS_SEEKABLE
    /* Seek to start position */
    if (params->start_time_ts > 0) {
        if (av_seek_frame(decoder_context->format_context,
                decoder_context->video_stream_index, params->start_time_ts, SEEK_SET) < 0) {
            elv_err("Failed seeking to desired start frame, url=%s", params->url);
            rc = eav_seek;
            goto xc_done;
        }
    }
#endif

    if (params->start_time_ts != -1) {
        if (params->xc_type == xc_video)
            encoder_context->format_context->start_time = params->start_time_ts;
        if (params->xc_type & xc_audio) {
            for (int i=0; i<encoder_context->n_audio_output; i++)
               encoder_context->format_context2[i]->start_time = params->start_time_ts;
        }
        /* PENDING (RM) add new start_time_ts for audio */
    }

    decoder_context->video_input_start_pts = AV_NOPTS_VALUE;
    decoder_context->video_duration = -1;
    encoder_context->audio_duration = -1;
    encoder_context->video_encoder_prev_pts = -1;
    decoder_context->first_decoding_video_pts = AV_NOPTS_VALUE;
    encoder_context->first_encoding_video_pts = -1;
    encoder_context->video_pts = AV_NOPTS_VALUE;

    for (int j=0; j<MAX_STREAMS; j++) {
        decoder_context->first_decoding_audio_pts[j] = AV_NOPTS_VALUE;
        encoder_context->first_encoding_audio_pts[j] = AV_NOPTS_VALUE;
        decoder_context->audio_input_start_pts[j] = AV_NOPTS_VALUE;
        encoder_context->audio_pts[j] = AV_NOPTS_VALUE;
        encoder_context->first_read_packet_pts[j] = AV_NOPTS_VALUE;
        encoder_context->audio_last_pts_sent_encode[j] = AV_NOPTS_VALUE;
    }
    decoder_context->first_key_frame_pts = AV_NOPTS_VALUE;
    decoder_context->is_av_synced = 0;
    encoder_context->video_last_pts_sent_encode = -1;

    int64_t video_last_dts = 0;
    int frames_read_past_duration = 0;
    const int frames_allowed_past_duration = 5;

    /* If there is a transcoding error, break the main loop */
    while (!xctx->err) {
        input_packet = av_packet_alloc();
        if (!input_packet) {
            elv_err("Failed to allocated memory for AVPacket, url=%s", params->url);
            return eav_mem_alloc;
        }

        rc = av_read_frame(decoder_context->format_context, input_packet);
        if (rc < 0) {
            av_packet_free(&input_packet);
            av_read_frame_rc = rc;
            if (rc == AVERROR_EOF || rc == -1)
                rc = eav_success;
            else {
                elv_err("av_read_frame() rc=%d, url=%s", rc, params->url);
                rc = eav_read_input;
            }
            break;
        }

        if (input_packet->flags & AV_PKT_FLAG_CORRUPT) {
            elv_warn("packet corrupt pts=%"PRId64, input_packet->pts);
            av_packet_free(&input_packet);
            continue;
        }

        const char *st = stream_type_str(encoder_context, input_packet->stream_index);
        int stream_index = input_packet->stream_index;

        // Record PTS of first frame read - excute only for the desired stream
        if ((stream_index == decoder_context->video_stream_index && (params->xc_type & xc_video)) ||
            (selected_decoded_audio(decoder_context, stream_index) >= 0 && (params->xc_type & xc_audio))) {

            if (encoder_context->first_read_packet_pts[stream_index] == AV_NOPTS_VALUE && input_packet->pts != AV_NOPTS_VALUE) {
                encoder_context->first_read_packet_pts[stream_index] = input_packet->pts;
                elv_log("PTS first_read_packet_pts=%"PRId64" stream=%d:%d:%s sidx=%d, url=%s",
                    encoder_context->first_read_packet_pts[stream_index], params->xc_type, params->stream_id,
                    st, stream_index, params->url);
            } else if (input_packet->pts != AV_NOPTS_VALUE && input_packet->pts < encoder_context->first_read_packet_pts[stream_index]) {
                /* Due to b-frame reordering */
                encoder_context->first_read_packet_pts[stream_index] = input_packet->pts;
                elv_log("PTS first_read_packet reorder new=%"PRId64" stream=%d:%d:%s, url=%s",
                    encoder_context->first_read_packet_pts, params->xc_type, params->stream_id, st, params->url);
            }
        }

        /* This is a very special case, sometimes pts is not set but dts has a value. */
        if (input_packet->pts == AV_NOPTS_VALUE &&
            input_packet->dts != AV_NOPTS_VALUE)
            input_packet->pts = input_packet->dts;

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
        if ((input_packet->stream_index == decoder_context->video_stream_index && (params->xc_type & xc_video)) ||
            (selected_decoded_audio(decoder_context, input_packet->stream_index) >= 0 && (params->xc_type & xc_audio))) {
            // Stop when we reached the desired duration (duration -1 means 'entire input stream')
            // PENDING(SSS) - this logic can be done after decoding where we know concretely that we decoded all frames
            // we need to encode.
            if (should_stop_decoding(input_packet, decoder_context, encoder_context,
                    params, inctx->audio_frames_read, inctx->video_frames_read,
                    &frames_read_past_duration, frames_allowed_past_duration)) {
                av_packet_free(&input_packet);
                if (decoder_context->cancelled)
                    rc = eav_cancelled;
                break;
            }

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
                (params->xc_type == xc_video || params->xc_type == xc_audio) &&
                skip_until_start_time_pts(decoder_context, input_packet, params)) {
                    av_packet_unref(input_packet);
                    av_packet_free(&input_packet);
                    continue;
            }
        }

        if (input_packet->stream_index == decoder_context->video_stream_index &&
            (params->xc_type & xc_video)) {
            // Video packet
            dump_packet(0, "IN ", input_packet, debug_frame_level);

            inctx->video_frames_read++;
            if (in_handlers->avpipe_stater)
                in_handlers->avpipe_stater(inctx, input_packet->stream_index, in_stat_video_frame_read);

            if (decoder_context->first_key_frame_pts == AV_NOPTS_VALUE &&
                    input_packet->flags == AV_PKT_FLAG_KEY) {
                decoder_context->first_key_frame_pts = input_packet->pts;
                decoder_context->inctx->first_key_frame_pts = decoder_context->first_key_frame_pts;
                elv_log("PTS first_key_frame_pts=%"PRId64" sidx=%d flags=%d dts=%"PRId64,
                    decoder_context->first_key_frame_pts, input_packet->stream_index, input_packet->flags, input_packet->dts);
                if (in_handlers->avpipe_stater)
                    in_handlers->avpipe_stater(decoder_context->inctx, input_packet->stream_index, in_stat_first_keyframe_pts);
            }

            // Assert DTS is growing as expected (accommodate non integer and irregular frame duration)
            if (video_last_dts + input_packet->duration * 1.5 < input_packet->dts &&
                video_last_dts != 0 && input_packet->duration > 0) {
                elv_log("Expected dts == last_dts + duration - video_last_dts=%" PRId64 " duration=%" PRId64 " dts=%" PRId64 " url=%s",
                    video_last_dts, input_packet->duration, input_packet->dts, params->url);
            }
            video_last_dts = input_packet->dts;

            xc_frame_t *xc_frame = (xc_frame_t *) calloc(1, sizeof(xc_frame_t));
            xc_frame->packet = input_packet;
            xc_frame->stream_index = input_packet->stream_index;
            elv_channel_send(xctx->vc, xc_frame);

        } else if (selected_decoded_audio(decoder_context, input_packet->stream_index) >= 0 &&
            params->xc_type & xc_audio) {

            encoder_context->audio_last_dts[input_packet->stream_index] = input_packet->dts;

            dump_packet(1, "IN ", input_packet, debug_frame_level);

            inctx->audio_frames_read++;
            if (in_handlers->avpipe_stater)
                in_handlers->avpipe_stater(inctx, input_packet->stream_index, in_stat_audio_frame_read);

            xc_frame_t *xc_frame = (xc_frame_t *) calloc(1, sizeof(xc_frame_t));
            xc_frame->packet = input_packet;
            xc_frame->stream_index = input_packet->stream_index;
            elv_channel_send(xctx->ac, xc_frame);

        } else {
            if (stream_index == decoder_context->data_scte35_stream_index) {
                uint8_t scte35_command_type;
                int res = parse_scte35_pkt(&scte35_command_type, input_packet);
                if (res < 0) {
                    elv_warn("SCTE [%d] fail to parse pts=%"PRId64" size=%d",
                        input_packet->stream_index, input_packet->pts, input_packet->size);
                } else {
                    char hex_str[2 * input_packet->size + 1];
                    switch (scte35_command_type) {
                    case 4:
                    case 5:
                    case 6:
                        hex_encode(input_packet->data, input_packet->size, hex_str);
                        elv_dbg("SCTE [%d] pts=%"PRId64" duration=%"PRId64" flag=%d size=%d "
                            "data=%s command=%d",
                            input_packet->stream_index, input_packet->pts, input_packet->duration,
                            input_packet->flags, input_packet->size,
                            hex_str, scte35_command_type);

                        if (in_handlers->avpipe_stater) {
                            inctx->data = (uint8_t *)hex_str;
                            in_handlers->avpipe_stater(inctx, input_packet->stream_index, in_stat_data_scte35);
                        }
                        break;
                    }
                }
            } else {
                if (debug_frame_level)
                    elv_dbg("Skip stream - packet index=%d, pts=%"PRId64", url=%s",
                        input_packet->stream_index, input_packet->pts, params->url);
            }
            av_packet_free(&input_packet);
        }
    }

xc_done:
    elv_dbg("av_read_frame() av_read_frame_rc=%d, rc=%d, url=%s", av_read_frame_rc, rc, params->url);

    xctx->stop = 1;
    /* Don't purge the channels, let the receiver to drain it */
    elv_channel_close(xctx->vc, 0);
    elv_channel_close(xctx->ac, 0);
    pthread_join(xctx->vthread_id, NULL);
    pthread_join(xctx->athread_id, NULL);

    /*
     * Flush all frames, first flush decoder buffers, then encoder buffers by passing NULL frame.
     */
    if (params->xc_type & xc_video && xctx->err != eav_write_frame)
        flush_decoder(decoder_context, encoder_context, encoder_context->video_stream_index, params, debug_frame_level);
    if (params->xc_type & xc_audio && xctx->err != eav_write_frame) {
        for (int i=0; i<decoder_context->n_audio; i++)
            flush_decoder(decoder_context, encoder_context, encoder_context->audio_stream_index[i], params, debug_frame_level);
    }
    if (params->xc_type & xc_audio_join || params->xc_type & xc_audio_merge) {
        for (int i=0; i<decoder_context->n_audio; i++)
            flush_decoder(decoder_context, encoder_context, decoder_context->audio_stream_index[i], params, debug_frame_level);
    }

    if (!params->bypass_transcoding && (params->xc_type & xc_video) && xctx->err != eav_write_frame)
        encode_frame(decoder_context, encoder_context, NULL, decoder_context->video_stream_index, params, debug_frame_level);
    /* Loop through and flush all audio frames */
    if (!params->bypass_transcoding && params->xc_type & xc_audio && xctx->err != eav_write_frame) {
        for (int i=0; i<decoder_context->n_audio; i++)
            encode_frame(decoder_context, encoder_context, NULL, decoder_context->audio_stream_index[i], params, debug_frame_level);
    }

    dump_trackers(decoder_context->format_context, encoder_context->format_context);

    if ((params->xc_type & xc_video) && rc == eav_success)
        av_write_trailer(encoder_context->format_context);
    if ((params->xc_type & xc_audio) && rc == eav_success) {
        for (int i=0; i<encoder_context->n_audio_output; i++)
            av_write_trailer(encoder_context->format_context2[i]);
    }

    char audio_last_dts_buf[(MAX_STREAMS + 1) * 20];
    char audio_input_start_pts_buf[(MAX_STREAMS + 1) * 20];
    char audio_last_pts_read_buf[(MAX_STREAMS + 1) * 20];
    char audio_last_pts_sent_encode_buf[(MAX_STREAMS + 1) * 20];
    char audio_last_pts_encoded_buf[(MAX_STREAMS + 1) * 20];
    audio_last_dts_buf[0] = '\0';
    audio_input_start_pts_buf[0] = '\0';
    audio_last_pts_read_buf[0] = '\0';
    audio_last_pts_sent_encode_buf[0] = '\0';
    audio_last_pts_encoded_buf[0] = '\0';
    for (int i=0; i<params->n_audio; i++) {
        char buf[32];
        int audio_index = params->audio_index[i];
        if (i > 0) {
            strncat(audio_last_dts_buf, ",", (MAX_STREAMS + 1) * 20 - strlen(audio_last_dts_buf));
            strncat(audio_input_start_pts_buf, ",", (MAX_STREAMS + 1) * 20 - strlen(audio_input_start_pts_buf));
            strncat(audio_last_pts_read_buf, ",", (MAX_STREAMS + 1) * 20 - strlen(audio_last_pts_read_buf));
            strncat(audio_last_pts_sent_encode_buf, ",", (MAX_STREAMS + 1) * 20 - strlen(audio_last_pts_sent_encode_buf));
            strncat(audio_last_pts_encoded_buf, ",", (MAX_STREAMS + 1) * 20 - strlen(audio_last_pts_encoded_buf));
        }
        sprintf(buf, "%"PRId64, encoder_context->audio_last_dts[audio_index]);
        strncat(audio_last_dts_buf, buf, (MAX_STREAMS + 1) * 20 - strlen(audio_last_dts_buf));
        sprintf(buf, "%"PRId64, encoder_context->audio_input_start_pts[audio_index]);
        strncat(audio_input_start_pts_buf, buf, (MAX_STREAMS + 1) * 20 - strlen(audio_input_start_pts_buf));
        sprintf(buf, "%"PRId64, encoder_context->audio_last_pts_read[audio_index]);
        strncat(audio_last_pts_read_buf, buf, (MAX_STREAMS + 1) * 20 - strlen(audio_last_pts_read_buf));
        sprintf(buf, "%"PRId64, encoder_context->audio_last_pts_sent_encode[audio_index]);
        strncat(audio_last_pts_sent_encode_buf, buf, (MAX_STREAMS + 1) * 20 - strlen(audio_last_pts_sent_encode_buf));
        sprintf(buf, "%"PRId64, encoder_context->audio_last_pts_encoded[audio_index]);
        strncat(audio_last_pts_encoded_buf, buf, (MAX_STREAMS + 1) * 20 - strlen(audio_last_pts_encoded_buf));
    } 

    elv_log("avpipe_xc done url=%s, rc=%d, xctx->err=%d, xc-type=%d, "
        "last video_pts=%"PRId64" audio_pts=%"PRId64
        " video_input_start_pts=%"PRId64" audio_input_start_pts=[%s]"
        " video_last_dts=%"PRId64" audio_last_dts=[%s]"
        " video_last_pts_read=%"PRId64" audio_last_pts_read=[%s]"
        " video_pts_sent_encode=%"PRId64" audio_last_pts_sent_encode=[%s]"
        " last_pts_encoded=%"PRId64" audio_last_pts_encoded=[%s]",
        params->url,
        rc, xctx->err, params->xc_type,
        encoder_context->video_pts,
        encoder_context->audio_pts,
        encoder_context->video_input_start_pts,
        audio_input_start_pts_buf,
        encoder_context->video_last_dts,
        audio_last_dts_buf,
        encoder_context->video_last_pts_read,
        audio_last_pts_read_buf,
        encoder_context->video_last_pts_sent_encode,
        audio_last_pts_sent_encode_buf,
        encoder_context->video_last_pts_encoded,
        audio_last_pts_encoded_buf);

    decoder_context->stopped = 1;
    encoder_context->stopped = 1;

    if (decoder_context->cancelled) {
        elv_warn("transcoding session cancelled, handle=%d, url=%s", xctx->handle, params->url);
        return eav_cancelled;
    }

    /* If there was an error in reading frames, return that error */
    if (rc != eav_success)
        return rc;

    /* Return transcoding error code */
    return xctx->err;
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
        return "";
    return channel_names[channel_layout].name;
}

const char*
avpipe_channel_layout_name(
    int channel_layout)
{
    for (int i = 0; i < sizeof(channel_layout_map)/sizeof(channel_layout_map[0]); i++) {
        if (channel_layout_map[i].layout == channel_layout)
            return channel_layout_map[i].name;
    }

    return "";
}

static int
get_channel_layout_for_encoder(int channel_layout)
{
    switch (channel_layout) {
    case AV_CH_LAYOUT_2_1:
        channel_layout = AV_CH_LAYOUT_SURROUND;
        break;
    case AV_CH_LAYOUT_2_2:
        channel_layout = AV_CH_LAYOUT_QUAD;
        break;
    case AV_CH_LAYOUT_5POINT0:
        channel_layout = AV_CH_LAYOUT_5POINT0_BACK;
        break;
    case AV_CH_LAYOUT_5POINT1:
        channel_layout = AV_CH_LAYOUT_5POINT1_BACK;
        break;
    case AV_CH_LAYOUT_6POINT0_FRONT:
        channel_layout = AV_CH_LAYOUT_6POINT0;
        break;
    case AV_CH_LAYOUT_6POINT1_BACK:
    case AV_CH_LAYOUT_6POINT1_FRONT:
        channel_layout = AV_CH_LAYOUT_6POINT1;
        break;
    case AV_CH_LAYOUT_7POINT0_FRONT:
        channel_layout = AV_CH_LAYOUT_7POINT0;
        break;
    case AV_CH_LAYOUT_7POINT1_WIDE_BACK:
    case AV_CH_LAYOUT_7POINT1_WIDE:
        channel_layout = AV_CH_LAYOUT_7POINT1;
        break;
    }

    /* If there is no input channel layout, set the default encoder channel layout to stereo */
    if (channel_layout == 0)
        channel_layout = AV_CH_LAYOUT_STEREO;

    return channel_layout;
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

    for (int i = 0; i < 64; i++) {
        if ((channel_layout & (UINT64_C(1) << i))) {
            const char *name = get_channel_name(i);
            if (name)
                return name;
        }
    }

    return "";
}

static const char*
get_xc_type_name(
    xc_type_t xc_type)
{
    switch (xc_type) {
    case xc_video:
        return "xc_video";
    case xc_audio:
        return "xc_audio";
    case xc_all:
        return "xc_all";
    case xc_audio_join:
        return "xc_audio_join";
    case xc_audio_pan:
        return "xc_audio_pan";
    case xc_audio_merge:
        return "xc_audio_merge";
    case xc_extract_images:
        return "xc_extract_images";
    case xc_extract_all_images:
        return "xc_extract_all_images";
    default:
        return "none";
    }

    return "none";
}

int
avpipe_probe(
    avpipe_io_handler_t *in_handlers,
    xcparams_t *params,
    xcprobe_t **xcprobe,
    int *n_streams)
{
    ioctx_t inctx;
    coderctx_t decoder_ctx;
    stream_info_t *stream_probes, *stream_probes_ptr;
    xcprobe_t *probe;
    int rc = 0;
    char *url;

    memset(&inctx, 0, sizeof(ioctx_t));
    memset(&decoder_ctx, 0, sizeof(coderctx_t));

    if (!params) {
        elv_err("avpipe_probe parameters are not set");
        rc = eav_param;
        goto avpipe_probe_end;
    }

    url = params->url;
    // Disable sync audio to stream id when probing
    params->sync_audio_to_stream_id = -1;
    params->stream_id = -1;

    if (!in_handlers) {
        elv_err("avpipe_probe NULL handlers, url=%s", url);
        rc = eav_param;
        goto avpipe_probe_end;
    }

    inctx.params = params;
    if (in_handlers->avpipe_opener(url, &inctx) < 0) {
        rc = eav_open_input;
        goto avpipe_probe_end;
    }

    if ((rc = prepare_decoder(&decoder_ctx, in_handlers, &inctx, params, params->seekable)) != eav_success) {
        elv_err("avpipe_probe failed to prepare decoder, url=%s", url);
        goto avpipe_probe_end;
    }

    int nb_streams = decoder_ctx.format_context->nb_streams;
    if (nb_streams <= 0) {
        rc = eav_num_streams;
        goto avpipe_probe_end;
    }

    int nb_skipped_streams = 0;
    probe = (xcprobe_t *)calloc(1, sizeof(xcprobe_t));
    stream_probes = (stream_info_t *)calloc(1, sizeof(stream_info_t)*nb_streams);
    for (int i=0; i<nb_streams; i++) {
        AVStream *s = decoder_ctx.format_context->streams[i];
        AVCodecContext *codec_context = decoder_ctx.codec_context[i];
        AVCodec *codec = decoder_ctx.codec[i];
        AVRational sar, dar;

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

        if (probe->container_info.duration <
            ((float)stream_probes_ptr->duration_ts)/stream_probes_ptr->time_base.den)
            probe->container_info.duration =
                ((float)stream_probes_ptr->duration_ts)/stream_probes_ptr->time_base.den;

        av_dict_copy(&stream_probes_ptr->tags, s->metadata, 0);

        for (int i = 0; i < s->nb_side_data; i++) {
            const AVPacketSideData *sd = &s->side_data[i];
            switch (sd->type) {
                case AV_PKT_DATA_DISPLAYMATRIX:
                    stream_probes_ptr->side_data.display_matrix.rotation = av_display_rotation_get((int32_t *)sd->data);
                    double rot = stream_probes_ptr->side_data.display_matrix.rotation;
                    // Convert from CCW [-180:180] value to straight CW
                    rot = rot >= 0 ? rot : 360.0 + rot;
                    rot = rot > 0 ? 360 - rot : 0;
                    stream_probes_ptr->side_data.display_matrix.rotation_cw = rot;
                    break;
                default:
                    // Not handled
                    break;
            }
        }
    }

    inctx.closed = 1;
    probe->stream_info = stream_probes;
    probe->container_info.format_name = strdup(decoder_ctx.format_context->iformat->name);
    *xcprobe = probe;
    *n_streams = nb_streams - nb_skipped_streams;

avpipe_probe_end:
    if (decoder_ctx.format_context) {
        if (decoder_ctx.format_context->pb->buffer){
            AVIOContext *avioctx = (AVIOContext *) decoder_ctx.format_context->pb;
            if (avioctx) {
                av_freep(&avioctx->buffer);
                av_freep(&avioctx);
            }
	}
        avformat_close_input(&decoder_ctx.format_context);
    }

    for (int i=0; i<MAX_STREAMS; i++) {
        if (decoder_ctx.codec_context[i]) {
            /* Corresponds to avcodec_open2() */
            avcodec_close(decoder_ctx.codec_context[i]);
            avcodec_free_context(&decoder_ctx.codec_context[i]);
        }
    }

    /* Close input handler resources */
    in_handlers->avpipe_closer(&inctx);

    return rc;
}

int avpipe_probe_free(xcprobe_t *probe, int n_streams) {
    if (probe == NULL)
        return 0;

    for (int i=0; i<n_streams; i++) {
        av_dict_free(&probe->stream_info[i].tags);
    }
    free(probe->stream_info);

    free(probe);
    return 0;
}

static int
check_params(
    xcparams_t *params)
{
    if (!params->url) {
        elv_err("Invalid params, url is null");
        return eav_param;
    }

    if (params->stream_id >= 0 && (params->xc_type != xc_none || params->n_audio > 0)) {
        elv_err("Incompatible params, stream_id=%d, xc_type=%d, n_audio=%d, url=%s",
            params->stream_id, params->xc_type, params->n_audio, params->url);
        return eav_param;
    }

    // By default transcode 'everything' if streamId < 0
    if (params->xc_type == xc_none && params->stream_id < 0) {
        params->xc_type = xc_all;
    }

    if (params->start_pts < 0) {
        elv_err("Start PTS can not be negative, url=%s", params->url);
        return eav_param;
    }

    if (params->watermark_text != NULL && (strlen(params->watermark_text) > (WATERMARK_STRING_SZ-1))){
        elv_err("Watermark too large, url=%s, wm_text size=%d", params->url, (int) strlen(params->watermark_text));
        return eav_param;
    }

    /*
     * Automatically set encoder rate control parameters: Currently constrains
     * bit rate over a 1 second interval (bufsize == maxrate) to the average bit
     * rate (video_bitrate). For CRF/VBV (crf_str instead of video_bitrate),
     * the RC paramters limit bitrate spikes.
     *
     * Increasing the bufsize may improve image quality at the cost of increased
     * bitrate variation.
     *
     * References:
     *   - https://trac.ffmpeg.org/wiki/Limiting%20the%20output%20bitrate
     *   - https://trac.ffmpeg.org/wiki/Encode/H.264
     */
    if (params->video_bitrate > 0) {
        if (params->video_bitrate > params->rc_max_rate) {
            elv_log("Replacing rc_max_rate %d with video_bitrate %d, url=%s",
                params->rc_max_rate, params->video_bitrate, params->url);
            params->rc_max_rate = params->video_bitrate;
        }
        if (params->video_bitrate > params->rc_buffer_size) {
            elv_log("Replacing rc_buffer_size %d with video_bitrate %d, url=%s",
                params->rc_buffer_size, params->video_bitrate, params->url);
            params->rc_buffer_size = params->video_bitrate;
        }
    }

    /*
     * PENDING (RM), this is just a short cut to convert joining the same MONO audio index
     * into a normal audio transcoding and produce stereo (this will prevent a crash).
     */
    if (params->xc_type == xc_audio_join &&
        params->n_audio == 2 &&
        params->audio_index[0] == params->audio_index[1]) {
        params->xc_type = xc_audio;
        params->channel_layout = AV_CH_LAYOUT_STEREO;
        params->n_audio = 1;
    }

    if (params->bitdepth == 0) {
        params->bitdepth = 8;
        elv_log("Set bitdepth=%d, url=%s", params->bitdepth, params->url);
    }

    if (params->xc_type & xc_audio &&
        params->sample_rate > 0 &&
        !strcmp(params->ecodec2, "aac") &&
        !is_valid_aac_sample_rate(params->sample_rate)) {
        elv_err("Invalid sample_rate for aac encoder, url=%s", params->url);
        return eav_param;
    }

    if (params->xc_type & xc_audio &&
        params->seg_duration <= 0 &&
        params->audio_seg_duration_ts <= 0 &&
        strcmp(params->format, "mp4")) {
        elv_err("Segment duration is not set for audio (invalid seg_duration and audio_seg_duration_ts), url=%s", params->url);
        return eav_param;
    }

    if (params->xc_type & xc_video &&
        params->xc_type != xc_extract_images &&
        params->xc_type != xc_extract_all_images &&
        params->seg_duration <= 0 &&
        params->video_seg_duration_ts <= 0 &&
        strcmp(params->format, "mp4")) {
        elv_err("Segment duration is not set for video (invalid seg_duration and video_seg_duration_ts), url=%s", params->url);
        return eav_param;
    }

    if (params->stream_id >=0 &&
        params->seg_duration <= 0) {
        elv_err("Segment duration is not set for stream id=%d, url=%s", params->stream_id, params->url);
        return eav_param;
    }

    if (params->n_audio > MAX_STREAMS) {
        elv_err("Too many audio indexes, n_audio=%d, url=%s", params->n_audio, params->url);
        return eav_param;
    }

    /* Set n_audio to zero if n_audio < 0 or xc_type == xc_video */
    if (params->n_audio < 0 ||
        params->xc_type == xc_video)
        params->n_audio = 0;

    if ((params->xc_type == xc_audio_join ||
        params->xc_type == xc_audio_merge) &&
        params->n_audio < 2) {
        elv_err("Insufficient audio indexes, n_audio=%d, xc_type=%d, url=%s",
            params->n_audio, params->xc_type, params->url);
        return eav_param;
    }

    if (params->extract_images_sz > 0) {
        if (params->extract_image_interval_ts > 0) {
            elv_err("Extract images either by interval or by frame list, url=%s", params->url);
            return eav_param;
        } else if (params->extract_images_ts == NULL) {
            elv_err("Frame list not set, url=%s", params->url);
            return eav_param;
        }
    }

    return eav_success;
}

int
avpipe_init(
    xctx_t **xctx,
    avpipe_io_handler_t *in_handlers,
    avpipe_io_handler_t *out_handlers,
    xcparams_t *p)
{
    xctx_t *p_xctx = NULL;
    xcparams_t *params = NULL;
    int rc = 0;
    ioctx_t *inctx = (ioctx_t *)calloc(1, sizeof(ioctx_t));

    if (!p) {
        elv_err("Parameters are not set");
        free(inctx);
        rc = eav_param;
        goto avpipe_init_failed;
    }

    params = (xcparams_t *) calloc(1, sizeof(xcparams_t));
    *params = *p;
    inctx->params = params;
    if (!p->url || p->url[0] == '\0' ||
        in_handlers->avpipe_opener(p->url, inctx) < 0) {
        elv_err("Failed to open avpipe input \"%s\"", p->url != NULL ? p->url : "");
        free(inctx);
        rc = eav_open_input;
        goto avpipe_init_failed;
    }

    if (!xctx) {
        elv_err("Trancoding context is NULL, url=%s", p->url);
        rc = eav_param;
        goto avpipe_init_failed;
    }

    p_xctx = (xctx_t *) calloc(1, sizeof(xctx_t));
    p_xctx->params = params;
    p_xctx->inctx = inctx;
    p_xctx->in_handlers = in_handlers;
    p_xctx->out_handlers = out_handlers;
    p_xctx->debug_frame_level = p->debug_frame_level;

    if (!params->format ||
        (strcmp(params->format, "dash") &&
         strcmp(params->format, "hls") &&
         strcmp(params->format, "image2") &&
         strcmp(params->format, "mp4") &&
         strcmp(params->format, "fmp4") &&
         strcmp(params->format, "segment") &&
         strcmp(params->format, "fmp4-segment"))) {
        elv_err("Output format can be only \"dash\", \"hls\", \"image2\", \"mp4\", \"fmp4\", \"segment\", or \"fmp4-segment\", url=%s", p->url);
        rc = eav_param;
        free(p_xctx);
        free(params);
        p_xctx = NULL;
        goto avpipe_init_failed;
    }

    char buf[4096];
    char audio_index_str[256];
    char index_str[10];

    audio_index_str[0] = '\0';
    for (int i=0; i<params->n_audio && i<MAX_STREAMS; i++) {
        snprintf(index_str, 10, "%d", params->audio_index[i]);
        strcat(audio_index_str, index_str);
        if (i < params->n_audio-1)
            strcat(audio_index_str, ",");
    }

    /* Note, when adding new params here, become sure buf is big enough to keep params */
    snprintf(buf, sizeof(buf),
        "stream_id=%d "
        "url=%s "
        "version=%s "
        "bypass=%d "
        "skip_decoding=%d "
        "xc_type=%s "
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
        "start_fragment_index=%d "
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
        "channel_layout=%d (%s) "
        "sync_audio_to_stream_id=%d "
        "wm_overlay_type=%d "
        "wm_overlay_len=%d "
        "bitdepth=%d "
        "listen=%d "
        "max_cll=\"%s\" "
        "master_display=\"%s\" "
        "filter_descriptor=\"%s\" "
        "extract_image_interval_ts=%"PRId64" "
        "extract_images_sz=%d "
        "video_time_base=%d/%d "
        "video_frame_duration_ts=%d "
        "rotate=%d",
        params->stream_id, p->url,
        avpipe_version(),
        params->bypass_transcoding, params->skip_decoding,
        get_xc_type_name(params->xc_type),
        params->format, params->seekable, params->start_time_ts,
        params->start_pts, params->duration_ts, params->start_segment_str,
        params->video_bitrate, params->audio_bitrate, params->sample_rate,
        params->crf_str, params->preset, params->rc_max_rate, params->rc_buffer_size,
        params->video_seg_duration_ts, params->audio_seg_duration_ts, params->seg_duration,
        params->start_fragment_index, params->force_keyint, params->force_equal_fduration,
        params->ecodec, params->ecodec2, params->dcodec, params->dcodec2,
        params->gpu_index, params->enc_height, params->enc_width,
        params->crypt_iv, params->crypt_key, params->crypt_kid, params->crypt_key_url,
        params->crypt_scheme, params->n_audio, audio_index_str,
        params->channel_layout, avpipe_channel_layout_name(params->channel_layout),
        params->sync_audio_to_stream_id,
        params->watermark_overlay_type, params->watermark_overlay_len,
        params->bitdepth, params->listen,
        params->max_cll ? params->max_cll : "",
        params->master_display ? params->master_display : "",
        params->filter_descriptor,
        params->extract_image_interval_ts, params->extract_images_sz,
        1, params->video_time_base, params->video_frame_duration_ts, params->rotate);
    elv_log("AVPIPE XCPARAMS %s", buf);

    if ((rc = check_params(params)) != eav_success) {
        goto avpipe_init_failed;
    }

    if ((rc = prepare_decoder(&p_xctx->decoder_ctx,
            in_handlers, inctx, params, params->seekable)) != eav_success) {
        elv_err("Failure in preparing decoder, url=%s, rc=%d", params->url, rc);
        goto avpipe_init_failed;
    }

    if ((rc = prepare_encoder(&p_xctx->encoder_ctx,
        &p_xctx->decoder_ctx, out_handlers, inctx, params)) != eav_success) {
        elv_err("Failure in preparing encoder, url=%s, rc=%d", params->url, rc);
        goto avpipe_init_failed;
    }

    elv_channel_init(&p_xctx->vc, 10000, NULL);
    elv_channel_init(&p_xctx->ac, 10000, NULL);

    /* Create threads for decoder and encoder */
    pthread_create(&p_xctx->vthread_id, NULL, transcode_video_func, p_xctx);
    pthread_create(&p_xctx->athread_id, NULL, transcode_audio_func, p_xctx);
    *xctx = p_xctx;

    return eav_success;

avpipe_init_failed:
    if (xctx)
        *xctx = NULL;
    if (p_xctx)
        avpipe_fini(&p_xctx);
    return rc;
}

static void
avpipe_free_params(
    xctx_t *xctx)
{
    xcparams_t *params = xctx->params;

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
    free(params->overlay_filename);
    free(params->watermark_overlay);
    free(params->watermark_shadow_color);
    free(params->watermark_timecode);
    free(params->max_cll);
    free(params->master_display);
    free(params->filter_descriptor);
    free(params->mux_spec);
    free(params->extract_images_ts);
    free(params);
    xctx->params = NULL;
}

int
avpipe_fini(
    xctx_t **xctx)
{
    coderctx_t *decoder_context;
    coderctx_t *encoder_context;

    if (!xctx || !(*xctx))
        return 0;

    if ((*xctx)->inctx && (*xctx)->inctx->url)
        elv_dbg("Releasing all the resources, url=%s", (*xctx)->inctx->url);

    /* Close input handler resources if it is not a muxing command */
    if (!(*xctx)->in_mux_ctx)
        (*xctx)->in_handlers->avpipe_closer((*xctx)->inctx);

    decoder_context = &(*xctx)->decoder_ctx;
    encoder_context = &(*xctx)->encoder_ctx;

    /* note: the internal buffer could have changed, and be != avio_ctx_buffer */
    if (decoder_context && decoder_context->format_context) {
        if ((*xctx)->inctx && strncmp((*xctx)->inctx->url, "rtmp://", 7) && strncmp((*xctx)->inctx->url, "srt://", 6)) {
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

    /* Free filter graph resources */
    if (decoder_context && decoder_context->video_filter_graph)
        avfilter_graph_free(&decoder_context->video_filter_graph);
    if (decoder_context && decoder_context->n_audio > 0) {
        for (int i=0; i<decoder_context->n_audio; i++)
            avfilter_graph_free(&decoder_context->audio_filter_graph[i]);
    }

    if (encoder_context && encoder_context->format_context) {
        void *avpipe_opaque = encoder_context->format_context->avpipe_opaque;
        avformat_free_context(encoder_context->format_context);
        free(avpipe_opaque);
    }
    if (encoder_context) {
        for (int i=0; i<encoder_context->n_audio_output; i++) { 
            void *avpipe_opaque = encoder_context->format_context2[i]->avpipe_opaque;
            avformat_free_context(encoder_context->format_context2[i]);
            free(avpipe_opaque);
        }
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

    if ((*xctx)->params && !strcmp((*xctx)->params->ecodec2, "aac")) {
        av_audio_fifo_free(decoder_context->fifo);
        swr_free(&decoder_context->resampler_context);
    }

    free((*xctx)->in_handlers);
    free((*xctx)->out_handlers);

    if ((*xctx)->inctx && (*xctx)->inctx->udp_channel)
        elv_channel_fini(&((*xctx)->inctx->udp_channel));
    free((*xctx)->inctx);
    elv_channel_fini(&((*xctx)->vc));
    elv_channel_fini(&((*xctx)->ac));

    avpipe_free_params(*xctx);
    free(*xctx);
    *xctx = NULL;

    return 0;
}

char *
avpipe_version()
{
    static char version_str[128];

    if (version_str[0] != '\0')
        return version_str;

#ifndef VERSION
#define VERSION "0.0.0-develop"
#endif

    snprintf(version_str, sizeof(version_str), "%d.%d@%s", AVPIPE_MAJOR_VERSION, AVPIPE_MINOR_VERSION, VERSION);
    return version_str;
}

void
init_extract_images(
    xcparams_t *params,
    int size)
{
    params->extract_images_ts = calloc(size, sizeof(int64_t));
    params->extract_images_sz = size;
}

void
set_extract_images(
    xcparams_t *params,
    int index,
    int64_t value)
{
    if (index >= params->extract_images_sz) {
        elv_err("set_extract_images - index out of bounds: %d, url=%s", index, params->url);
        return;
    }
    params->extract_images_ts[index] = value;
}
