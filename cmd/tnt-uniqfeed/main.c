/*
 * Copyright (c) 2010 Nicolas George
 * Copyright (c) 2011 Stefano Sabatini
 * Copyright (c) 2014 Andrey Utkin
 *
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to deal
 * in the Software without restriction, including without limitation the rights
 * to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
 * copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 *
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 *
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL
 * THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
 * OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
 * THE SOFTWARE.
 */

/**
 * @file
 * API example for demuxing, decoding, filtering, encoding and muxing
 * @example transcoding.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavutil/channel_layout.h>
#include <libavutil/imgutils.h>
#include <libavutil/opt.h>
#include <libavutil/pixdesc.h>

#include <inttypes.h>
#include <stdlib.h>
#include <stdio.h>
#include <string.h>

#ifdef USE_UF_RENDERLIB
#include <libswscale/swscale.h>

typedef struct UfContext UfContext;
typedef struct UfImage UfImage;
typedef struct UfMetadata UfMetadata;
typedef struct UfFeeds UfFeeds;
typedef enum UfImageFormat UfImageFormat;
#include <uf/renderlib/UfRenderInterface.h>
#endif

static AVFormatContext *ifmt_ctx;
static AVFormatContext *ofmt_ctx;
typedef struct FilteringContext {
    AVFilterContext *buffersink_ctx;
    AVFilterContext *buffersrc_ctx;
    AVFilterGraph *filter_graph;
} FilteringContext;
static FilteringContext *filter_ctx;

typedef struct StreamContext {
    AVCodecContext *dec_ctx;
    AVCodecContext *enc_ctx;
} StreamContext;
static StreamContext *stream_ctx;

static int filter_encode_write_frame(AVFrame *frame, unsigned int stream_index);

#if LIBAVUTIL_VERSION_MAJOR >= 57
static int ensure_channel_layout(AVCodecContext *ctx)
{
    int nb_channels = ctx->ch_layout.nb_channels;

    if (ctx->ch_layout.nb_channels && ctx->ch_layout.order != AV_CHANNEL_ORDER_UNSPEC)
        return 0;

    av_channel_layout_uninit(&ctx->ch_layout);
    av_channel_layout_default(&ctx->ch_layout, nb_channels > 0 ? nb_channels : 2);
    return 0;
}

static int describe_channel_layout(const AVCodecContext *ctx, char *buffer, size_t buffer_size)
{
    return av_channel_layout_describe(&ctx->ch_layout, buffer, buffer_size);
}

static int set_encoder_channel_layout(AVCodecContext *enc_ctx, const AVCodecContext *dec_ctx,
                                      const AVCodec *encoder)
{
    int channels = dec_ctx->ch_layout.nb_channels > 0 ? dec_ctx->ch_layout.nb_channels : 2;

    av_channel_layout_uninit(&enc_ctx->ch_layout);

    if (encoder->ch_layouts) {
        const AVChannelLayout *layout = encoder->ch_layouts;
        const AVChannelLayout *fallback = layout;

        for (; layout->nb_channels; layout++) {
            if (layout->nb_channels == channels)
                return av_channel_layout_copy(&enc_ctx->ch_layout, layout);
        }

        return av_channel_layout_copy(&enc_ctx->ch_layout, fallback);
    }

    if (dec_ctx->ch_layout.nb_channels && dec_ctx->ch_layout.order != AV_CHANNEL_ORDER_UNSPEC)
        return av_channel_layout_copy(&enc_ctx->ch_layout, &dec_ctx->ch_layout);

    av_channel_layout_default(&enc_ctx->ch_layout, channels);
    return 0;
}

#else
static int ensure_channel_layout(AVCodecContext *ctx)
{
    if (!ctx->channel_layout)
        ctx->channel_layout = av_get_default_channel_layout(ctx->channels > 0 ? ctx->channels : 2);
    return 0;
}

static int describe_channel_layout(const AVCodecContext *ctx, char *buffer, size_t buffer_size)
{
    av_get_channel_layout_string(buffer, buffer_size, ctx->channels, ctx->channel_layout);
    return 0;
}

static int set_encoder_channel_layout(AVCodecContext *enc_ctx, const AVCodecContext *dec_ctx,
                                      const AVCodec *encoder)
{
    int channels;
    uint64_t channel_layout = dec_ctx->channel_layout;

    channels = dec_ctx->channels > 0 ? dec_ctx->channels : 2;
    if (!channel_layout)
        channel_layout = av_get_default_channel_layout(channels);

    if (encoder->channel_layouts) {
        const uint64_t *layout = encoder->channel_layouts;
        uint64_t fallback = *layout;

        for (; *layout; layout++) {
            if (av_get_channel_layout_nb_channels(*layout) == channels) {
                channel_layout = *layout;
                break;
            }
        }

        if (!*layout)
            channel_layout = fallback;
    }

    enc_ctx->channel_layout = channel_layout;
    enc_ctx->channels = av_get_channel_layout_nb_channels(channel_layout);
    return 0;
}

#endif

#ifdef USE_UF_RENDERLIB
#define UF_RENDERLIB_EXPECTED_WIDTH 1280
#define UF_RENDERLIB_EXPECTED_HEIGHT 720

typedef struct RenderLibContext {
    UfContext *ctx;
    const char *metadata_dir;
    uint64_t *video_frame_count;
    uint64_t metadata_frame_count;
    int passthrough_on_failure;
    int render_disabled;
} RenderLibContext;

static RenderLibContext renderlib_ctx;

static int env_flag_enabled(const char *name)
{
    const char *value = getenv(name);

    if (!value || !*value)
        return 0;

    if (!strcmp(value, "0") || !strcmp(value, "false") || !strcmp(value, "FALSE") ||
        !strcmp(value, "no") || !strcmp(value, "NO") ||
        !strcmp(value, "off") || !strcmp(value, "OFF"))
        return 0;

    return 1;
}

static UfImage *create_render_image_from_frame(const AVFrame *frame)
{
    struct SwsContext *scale_ctx;
    UfImage *image;
    uint8_t *dst_data;
    uint32_t dst_stride;
    int ret;

    image = uFCreateImage(frame->width, frame->height, R8G8B8_UINT);
    if (!image)
        return NULL;

    ret = uFGetImageHostBuffer(image, (void **)&dst_data);
    if (ret != 0 || !dst_data) {
        uFDestroyImage(image);
        return NULL;
    }

    ret = uFGetImageStride(image, &dst_stride);
    if (ret != 0) {
        uFDestroyImage(image);
        return NULL;
    }

    scale_ctx = sws_getContext(frame->width, frame->height, frame->format,
                               frame->width, frame->height, AV_PIX_FMT_RGB24,
                               SWS_BILINEAR, NULL, NULL, NULL);
    if (!scale_ctx) {
        uFDestroyImage(image);
        return NULL;
    }

    {
        uint8_t *rgb_data[4] = { dst_data, NULL, NULL, NULL };
        int rgb_linesize[4] = { (int)dst_stride, 0, 0, 0 };

        sws_scale(scale_ctx, (const uint8_t * const *)frame->data, frame->linesize,
                  0, frame->height, rgb_data, rgb_linesize);
    }

    sws_freeContext(scale_ctx);
    return image;
}

static UfMetadata *load_render_metadata(const char *metadata_dir, uint64_t frame_index)
{
    FILE *file;
    UfMetadata *metadata;
    long metadata_size;
    char *filename;
    uint8_t *buffer = NULL;
    int filename_len;

    filename_len = snprintf(NULL, 0, "%s/md-%06" PRIu64 ".bin",
                            metadata_dir, frame_index);
    if (filename_len < 0)
        return uFCreateMetadata(NULL, 0);

    filename = av_malloc(filename_len + 1);
    if (!filename)
        return NULL;

    snprintf(filename, filename_len + 1, "%s/md-%06" PRIu64 ".bin",
             metadata_dir, frame_index);

    file = fopen(filename, "rb");
    if (!file) {
        metadata = uFCreateMetadata(NULL, 0);
        if (!metadata) {
            av_log(NULL, AV_LOG_ERROR,
                   "uniqFEED could not create fallback metadata after missing file %s\n",
                   filename);
        }
        av_free(filename);
        return metadata;
    }

    if (fseek(file, 0, SEEK_END) < 0) {
        fclose(file);
        av_free(filename);
        return uFCreateMetadata(NULL, 0);
    }

    metadata_size = ftell(file);
    if (metadata_size <= 0 || fseek(file, 0, SEEK_SET) < 0) {
        fclose(file);
        metadata = uFCreateMetadata(NULL, 0);
        if (!metadata) {
            av_log(NULL, AV_LOG_ERROR,
                   "uniqFEED could not create fallback metadata after invalid file %s\n",
                   filename);
        }
        av_free(filename);
        return metadata;
    }

    buffer = av_malloc(metadata_size);
    if (!buffer) {
        fclose(file);
        av_free(filename);
        return NULL;
    }

    if (fread(buffer, 1, metadata_size, file) != (size_t)metadata_size) {
        av_free(buffer);
        fclose(file);
        metadata = uFCreateMetadata(NULL, 0);
        if (!metadata) {
            av_log(NULL, AV_LOG_ERROR,
                   "uniqFEED could not create fallback metadata after short read from %s\n",
                   filename);
        }
        av_free(filename);
        return metadata;
    }

    fclose(file);
    metadata = uFCreateMetadata(buffer, metadata_size);
    if (!metadata) {
        av_log(NULL, AV_LOG_ERROR,
               "uniqFEED rejected metadata file %s (%ld bytes)\n",
               filename, metadata_size);
    }
    av_free(filename);
    av_free(buffer);
    return metadata;
}

static uint64_t count_render_metadata_frames(const char *metadata_dir)
{
    FILE *file;
    uint64_t frame_index = 0;

    while (1) {
        char filename[4096];

        if (snprintf(filename, sizeof(filename), "%s/md-%06" PRIu64 ".bin",
                     metadata_dir, frame_index) >= (int)sizeof(filename))
            break;

        file = fopen(filename, "rb");
        if (!file)
            break;

        fclose(file);
        frame_index++;
    }

    return frame_index;
}

static int create_frame_from_render_image(const UfImage *image, const AVFrame *source_frame,
                                          AVFrame **processed_frame)
{
    struct SwsContext *scale_ctx;
    UfImage *rgb_image = NULL;
    AVFrame *frame;
    uint8_t *rgb_data;
    uint32_t width;
    uint32_t height;
    uint32_t stride;
    UfImageFormat format;
    int ret;

    ret = uFGetImageFormat(image, &format);
    if (ret != 0)
        return AVERROR_EXTERNAL;

    if (format != R8G8B8_UINT) {
        rgb_image = uFConvertImage(image, R8G8B8_UINT);
        if (!rgb_image)
            return AVERROR_EXTERNAL;
        image = rgb_image;
    }

    if (uFGetImageSize(image, &width, &height) != 0 ||
        uFGetImageHostBuffer(image, (void **)&rgb_data) != 0 ||
        uFGetImageStride(image, &stride) != 0 || !rgb_data)
        return AVERROR_EXTERNAL;

    if ((int)width != source_frame->width || (int)height != source_frame->height) {
        ret = AVERROR(EINVAL);
        goto end;
    }

    frame = av_frame_alloc();
    if (!frame)
        return AVERROR(ENOMEM);

    frame->format = source_frame->format;
    frame->width = source_frame->width;
    frame->height = source_frame->height;

    ret = av_frame_get_buffer(frame, 32);
    if (ret < 0) {
        av_frame_free(&frame);
        return ret;
    }

    scale_ctx = sws_getContext(source_frame->width, source_frame->height, AV_PIX_FMT_RGB24,
                               source_frame->width, source_frame->height, source_frame->format,
                               SWS_BILINEAR, NULL, NULL, NULL);
    if (!scale_ctx) {
        av_frame_free(&frame);
        ret = AVERROR(EINVAL);
        goto end;
    }

    {
        const uint8_t *src_data[4] = { rgb_data, NULL, NULL, NULL };
        int src_linesize[4] = { (int)stride, 0, 0, 0 };

        sws_scale(scale_ctx, src_data, src_linesize, 0, source_frame->height,
                  frame->data, frame->linesize);
    }

    sws_freeContext(scale_ctx);

    ret = av_frame_copy_props(frame, source_frame);
    if (ret < 0) {
        av_frame_free(&frame);
        goto end;
    }

    frame->pts = source_frame->pts;
    frame->best_effort_timestamp = source_frame->best_effort_timestamp;
    *processed_frame = frame;
    ret = 0;

end:
    if (rgb_image)
        uFDestroyImage(rgb_image);
    return ret;
}

static int process_video_frame_with_renderlib(AVFrame *frame, unsigned int stream_index,
                                              AVFrame **processed_frame)
{
    const UfImage *feed_image;
    UfFeeds *feeds = NULL;
    UfImage *input_image = NULL;
    UfMetadata *metadata = NULL;
    int ret;
    uint64_t frame_index;
    int64_t render_tid;

    *processed_frame = NULL;

    if (frame->width != UF_RENDERLIB_EXPECTED_WIDTH ||
        frame->height != UF_RENDERLIB_EXPECTED_HEIGHT) {
        av_log(NULL, AV_LOG_ERROR,
               "uniqFEED requires filtered video frames to be exactly %dx%d; got %dx%d on stream #%u\n",
               UF_RENDERLIB_EXPECTED_WIDTH, UF_RENDERLIB_EXPECTED_HEIGHT,
               frame->width, frame->height, stream_index);
        return AVERROR(EINVAL);
    }

    frame_index = renderlib_ctx.video_frame_count[stream_index]++;
    render_tid = frame->pts == AV_NOPTS_VALUE ? (int64_t)frame_index : frame->pts;

    if (renderlib_ctx.metadata_frame_count > 0 &&
        frame_index >= renderlib_ctx.metadata_frame_count) {
        av_log(NULL, AV_LOG_WARNING,
               "uniqFEED metadata exhausted at frame %" PRIu64 " on stream #%u; available sample metadata covers frames [0, %" PRIu64 "]\n",
               frame_index, stream_index, renderlib_ctx.metadata_frame_count - 1);
        if (renderlib_ctx.passthrough_on_failure) {
            renderlib_ctx.render_disabled = 1;
            return 0;
        }
        return AVERROR(EINVAL);
    }

    input_image = create_render_image_from_frame(frame);
    if (!input_image) {
        ret = AVERROR_EXTERNAL;
        goto end;
    }

    metadata = load_render_metadata(renderlib_ctx.metadata_dir, frame_index);
    if (!metadata) {
        ret = AVERROR_EXTERNAL;
        goto end;
    }

    feeds = uFRenderFeeds(renderlib_ctx.ctx, metadata, render_tid, input_image);
    if (!feeds) {
        ret = 0;
        goto end;
    }

    feed_image = uFGetFeedsImage(feeds, 0);
    if (!feed_image) {
        ret = AVERROR_EXTERNAL;
        goto end;
    }

    ret = create_frame_from_render_image(feed_image, frame, processed_frame);

end:
    if (feeds)
        uFDestroyFeeds(feeds);
    if (metadata)
        uFDestroyMetadata(metadata);
    if (input_image)
        uFDestroyImage(input_image);
    return ret;
}

static int init_renderlib(int argc, char **argv)
{
    if (argc < 5) {
        av_log(NULL, AV_LOG_ERROR,
               "Usage: %s <input file> <output file> <project path> <metadata dir>\n",
               argv[0]);
        return AVERROR(EINVAL);
    }

    renderlib_ctx.ctx = uFCreateContext(argv[3]);
    if (!renderlib_ctx.ctx) {
        av_log(NULL, AV_LOG_ERROR, "Failed to initialize uniqFEED render context\n");
        return AVERROR_EXTERNAL;
    }

    renderlib_ctx.metadata_dir = argv[4];
    renderlib_ctx.metadata_frame_count = count_render_metadata_frames(renderlib_ctx.metadata_dir);
    renderlib_ctx.passthrough_on_failure = env_flag_enabled("UF_RENDERLIB_PASSTHROUGH_ON_FAILURE");
    renderlib_ctx.render_disabled = 0;
    renderlib_ctx.video_frame_count = av_mallocz_array(ifmt_ctx->nb_streams,
                                                       sizeof(*renderlib_ctx.video_frame_count));
    if (!renderlib_ctx.video_frame_count) {
        uFDestroyContext(renderlib_ctx.ctx);
        renderlib_ctx.ctx = NULL;
        return AVERROR(ENOMEM);
    }

    if (renderlib_ctx.passthrough_on_failure) {
        av_log(NULL, AV_LOG_INFO,
               "uniqFEED passthrough-on-failure enabled; recoverable render errors will disable uniqFEED and continue with original frames\n");
    }

    if (renderlib_ctx.metadata_frame_count > 0) {
        av_log(NULL, AV_LOG_INFO,
               "uniqFEED metadata coverage: %" PRIu64 " frame(s) available in %s\n",
               renderlib_ctx.metadata_frame_count, renderlib_ctx.metadata_dir);
    } else {
        av_log(NULL, AV_LOG_WARNING,
               "uniqFEED metadata coverage: no md-XXXXXX.bin files found in %s\n",
               renderlib_ctx.metadata_dir);
    }

    return 0;
}

static void free_renderlib(void)
{
    av_freep(&renderlib_ctx.video_frame_count);
    if (renderlib_ctx.ctx)
        uFDestroyContext(renderlib_ctx.ctx);
    renderlib_ctx.ctx = NULL;
    renderlib_ctx.metadata_dir = NULL;
    renderlib_ctx.metadata_frame_count = 0;
    renderlib_ctx.passthrough_on_failure = 0;
    renderlib_ctx.render_disabled = 0;
}
#endif

static int open_input_file(const char *filename)
{
    int ret;
    unsigned int i;

    ifmt_ctx = NULL;
    if ((ret = avformat_open_input(&ifmt_ctx, filename, NULL, NULL)) < 0) {
        av_log(NULL, AV_LOG_ERROR, "Cannot open input file\n");
        return ret;
    }

    if ((ret = avformat_find_stream_info(ifmt_ctx, NULL)) < 0) {
        av_log(NULL, AV_LOG_ERROR, "Cannot find stream information\n");
        return ret;
    }

    stream_ctx = av_mallocz_array(ifmt_ctx->nb_streams, sizeof(*stream_ctx));
    if (!stream_ctx)
        return AVERROR(ENOMEM);

    for (i = 0; i < ifmt_ctx->nb_streams; i++) {
        AVStream *stream = ifmt_ctx->streams[i];
        AVCodec *dec = avcodec_find_decoder(stream->codecpar->codec_id);
        AVCodecContext *codec_ctx;
        if (!dec) {
            av_log(NULL, AV_LOG_ERROR, "Failed to find decoder for stream #%u\n", i);
            return AVERROR_DECODER_NOT_FOUND;
        }
        codec_ctx = avcodec_alloc_context3(dec);
        if (!codec_ctx) {
            av_log(NULL, AV_LOG_ERROR, "Failed to allocate the decoder context for stream #%u\n", i);
            return AVERROR(ENOMEM);
        }
        ret = avcodec_parameters_to_context(codec_ctx, stream->codecpar);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Failed to copy decoder parameters to input decoder context "
                   "for stream #%u\n", i);
            return ret;
        }
        /* Reencode video & audio and remux subtitles etc. */
        if (codec_ctx->codec_type == AVMEDIA_TYPE_VIDEO
                || codec_ctx->codec_type == AVMEDIA_TYPE_AUDIO) {
            if (codec_ctx->codec_type == AVMEDIA_TYPE_VIDEO)
                codec_ctx->framerate = av_guess_frame_rate(ifmt_ctx, stream, NULL);
            /* Open decoder */
            ret = avcodec_open2(codec_ctx, dec, NULL);
            if (ret < 0) {
                av_log(NULL, AV_LOG_ERROR, "Failed to open decoder for stream #%u\n", i);
                return ret;
            }
        }
        stream_ctx[i].dec_ctx = codec_ctx;
    }

    av_dump_format(ifmt_ctx, 0, filename, 0);
    return 0;
}

static int open_output_file(const char *filename)
{
    AVStream *out_stream;
    AVStream *in_stream;
    AVCodecContext *dec_ctx, *enc_ctx;
    AVCodec *encoder;
    int ret;
    unsigned int i;

    ofmt_ctx = NULL;
    avformat_alloc_output_context2(&ofmt_ctx, NULL, NULL, filename);
    if (!ofmt_ctx) {
        av_log(NULL, AV_LOG_ERROR, "Could not create output context\n");
        return AVERROR_UNKNOWN;
    }


    for (i = 0; i < ifmt_ctx->nb_streams; i++) {
        out_stream = avformat_new_stream(ofmt_ctx, NULL);
        if (!out_stream) {
            av_log(NULL, AV_LOG_ERROR, "Failed allocating output stream\n");
            return AVERROR_UNKNOWN;
        }

        in_stream = ifmt_ctx->streams[i];
        dec_ctx = stream_ctx[i].dec_ctx;

        if (dec_ctx->codec_type == AVMEDIA_TYPE_VIDEO
                || dec_ctx->codec_type == AVMEDIA_TYPE_AUDIO) {
            /* in this example, we choose transcoding to same codec */
            encoder = avcodec_find_encoder(dec_ctx->codec_id);
            if (!encoder) {
                av_log(NULL, AV_LOG_FATAL, "Necessary encoder not found\n");
                return AVERROR_INVALIDDATA;
            }
            enc_ctx = avcodec_alloc_context3(encoder);
            if (!enc_ctx) {
                av_log(NULL, AV_LOG_FATAL, "Failed to allocate the encoder context\n");
                return AVERROR(ENOMEM);
            }

            /* In this example, we transcode to same properties (picture size,
             * sample rate etc.). These properties can be changed for output
             * streams easily using filters */
            if (dec_ctx->codec_type == AVMEDIA_TYPE_VIDEO) {
                enc_ctx->height = dec_ctx->height;
                enc_ctx->width = dec_ctx->width;
                enc_ctx->sample_aspect_ratio = dec_ctx->sample_aspect_ratio;
                /* take first format from list of supported formats */
                if (encoder->pix_fmts)
                    enc_ctx->pix_fmt = encoder->pix_fmts[0];
                else
                    enc_ctx->pix_fmt = dec_ctx->pix_fmt;
                /* video time_base can be set to whatever is handy and supported by encoder */
                enc_ctx->time_base = av_inv_q(dec_ctx->framerate);
            } else {
                enc_ctx->sample_rate = dec_ctx->sample_rate;
                ret = set_encoder_channel_layout(enc_ctx, dec_ctx, encoder);
                if (ret < 0) {
                    av_log(NULL, AV_LOG_ERROR, "Failed to set output channel layout for stream #%u\n", i);
                    return ret;
                }
                /* take first format from list of supported formats */
                enc_ctx->sample_fmt = encoder->sample_fmts[0];
                enc_ctx->time_base = (AVRational){1, enc_ctx->sample_rate};
            }

            if (ofmt_ctx->oformat->flags & AVFMT_GLOBALHEADER)
                enc_ctx->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

            /* Third parameter can be used to pass settings to encoder */
            ret = avcodec_open2(enc_ctx, encoder, NULL);
            if (ret < 0) {
                av_log(NULL, AV_LOG_ERROR, "Cannot open video encoder for stream #%u\n", i);
                return ret;
            }
            ret = avcodec_parameters_from_context(out_stream->codecpar, enc_ctx);
            if (ret < 0) {
                av_log(NULL, AV_LOG_ERROR, "Failed to copy encoder parameters to output stream #%u\n", i);
                return ret;
            }

            out_stream->time_base = enc_ctx->time_base;
            stream_ctx[i].enc_ctx = enc_ctx;
        } else if (dec_ctx->codec_type == AVMEDIA_TYPE_UNKNOWN) {
            av_log(NULL, AV_LOG_FATAL, "Elementary stream #%d is of unknown type, cannot proceed\n", i);
            return AVERROR_INVALIDDATA;
        } else {
            /* if this stream must be remuxed */
            ret = avcodec_parameters_copy(out_stream->codecpar, in_stream->codecpar);
            if (ret < 0) {
                av_log(NULL, AV_LOG_ERROR, "Copying parameters for stream #%u failed\n", i);
                return ret;
            }
            out_stream->time_base = in_stream->time_base;
        }

    }
    av_dump_format(ofmt_ctx, 0, filename, 1);

    if (!(ofmt_ctx->oformat->flags & AVFMT_NOFILE)) {
        ret = avio_open(&ofmt_ctx->pb, filename, AVIO_FLAG_WRITE);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Could not open output file '%s'", filename);
            return ret;
        }
    }

    /* init muxer, write output file header */
    ret = avformat_write_header(ofmt_ctx, NULL);
    if (ret < 0) {
        av_log(NULL, AV_LOG_ERROR, "Error occurred when opening output file\n");
        return ret;
    }

    return 0;
}

static int init_filter(FilteringContext* fctx, AVCodecContext *dec_ctx,
        AVCodecContext *enc_ctx, const char *filter_spec)
{
    char args[512];
    int ret = 0;
    const AVFilter *buffersrc = NULL;
    const AVFilter *buffersink = NULL;
    AVFilterContext *buffersrc_ctx = NULL;
    AVFilterContext *buffersink_ctx = NULL;
    AVFilterInOut *outputs = avfilter_inout_alloc();
    AVFilterInOut *inputs  = avfilter_inout_alloc();
    AVFilterGraph *filter_graph = avfilter_graph_alloc();

    if (!outputs || !inputs || !filter_graph) {
        ret = AVERROR(ENOMEM);
        goto end;
    }

    if (dec_ctx->codec_type == AVMEDIA_TYPE_VIDEO) {
        buffersrc = avfilter_get_by_name("buffer");
        buffersink = avfilter_get_by_name("buffersink");
        if (!buffersrc || !buffersink) {
            av_log(NULL, AV_LOG_ERROR, "filtering source or sink element not found\n");
            ret = AVERROR_UNKNOWN;
            goto end;
        }

        snprintf(args, sizeof(args),
                "video_size=%dx%d:pix_fmt=%d:time_base=%d/%d:pixel_aspect=%d/%d",
                dec_ctx->width, dec_ctx->height, dec_ctx->pix_fmt,
                dec_ctx->time_base.num, dec_ctx->time_base.den,
                dec_ctx->sample_aspect_ratio.num,
                dec_ctx->sample_aspect_ratio.den);

        ret = avfilter_graph_create_filter(&buffersrc_ctx, buffersrc, "in",
                args, NULL, filter_graph);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot create buffer source\n");
            goto end;
        }

        ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out",
                NULL, NULL, filter_graph);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot create buffer sink\n");
            goto end;
        }

        ret = av_opt_set_bin(buffersink_ctx, "pix_fmts",
                (uint8_t*)&enc_ctx->pix_fmt, sizeof(enc_ctx->pix_fmt),
                AV_OPT_SEARCH_CHILDREN);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot set output pixel format\n");
            goto end;
        }
    } else if (dec_ctx->codec_type == AVMEDIA_TYPE_AUDIO) {
        char dec_channel_layout[128];
#if LIBAVUTIL_VERSION_MAJOR >= 57
        AVChannelLayout out_ch_layouts[2];
#else
        int64_t out_channel_layouts[2];
#endif
        int out_sample_rates[2];

        buffersrc = avfilter_get_by_name("abuffer");
        buffersink = avfilter_get_by_name("abuffersink");
        if (!buffersrc || !buffersink) {
            av_log(NULL, AV_LOG_ERROR, "filtering source or sink element not found\n");
            ret = AVERROR_UNKNOWN;
            goto end;
        }

        ret = ensure_channel_layout(dec_ctx);
        if (ret < 0)
            goto end;
        ret = describe_channel_layout(dec_ctx, dec_channel_layout, sizeof(dec_channel_layout));
        if (ret < 0)
            goto end;

        snprintf(args, sizeof(args),
                "time_base=%d/%d:sample_rate=%d:sample_fmt=%s:channel_layout=%s",
                dec_ctx->time_base.num, dec_ctx->time_base.den, dec_ctx->sample_rate,
                av_get_sample_fmt_name(dec_ctx->sample_fmt),
                dec_channel_layout);
        ret = avfilter_graph_create_filter(&buffersrc_ctx, buffersrc, "in",
                args, NULL, filter_graph);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot create audio buffer source\n");
            goto end;
        }

        ret = avfilter_graph_create_filter(&buffersink_ctx, buffersink, "out",
                NULL, NULL, filter_graph);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot create audio buffer sink\n");
            goto end;
        }

        ret = av_opt_set_bin(buffersink_ctx, "sample_fmts",
                (uint8_t*)&enc_ctx->sample_fmt, sizeof(enc_ctx->sample_fmt),
                AV_OPT_SEARCH_CHILDREN);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot set output sample format\n");
            goto end;
        }

#if LIBAVUTIL_VERSION_MAJOR >= 57
        memset(out_ch_layouts, 0, sizeof(out_ch_layouts));
        ret = av_channel_layout_copy(&out_ch_layouts[0], &enc_ctx->ch_layout);
        if (ret < 0)
            goto end;

        ret = av_opt_set_chlayout(buffersink_ctx, "ch_layouts",
                &out_ch_layouts[0], AV_OPT_SEARCH_CHILDREN);
#else
        out_channel_layouts[0] = enc_ctx->channel_layout;
        out_channel_layouts[1] = -1;
        ret = av_opt_set_int_list(buffersink_ctx, "channel_layouts",
                out_channel_layouts, -1, AV_OPT_SEARCH_CHILDREN);
#endif
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot set output channel layout\n");
            goto end;
        }

        out_sample_rates[0] = enc_ctx->sample_rate;
        out_sample_rates[1] = -1;
        ret = av_opt_set_int_list(buffersink_ctx, "sample_rates",
            out_sample_rates, -1, AV_OPT_SEARCH_CHILDREN);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Cannot set output sample rate\n");
            goto end;
        }

#if LIBAVUTIL_VERSION_MAJOR >= 57
        av_channel_layout_uninit(&out_ch_layouts[0]);
#endif
    } else {
        ret = AVERROR_UNKNOWN;
        goto end;
    }

    /* Endpoints for the filter graph. */
    outputs->name       = av_strdup("in");
    outputs->filter_ctx = buffersrc_ctx;
    outputs->pad_idx    = 0;
    outputs->next       = NULL;

    inputs->name       = av_strdup("out");
    inputs->filter_ctx = buffersink_ctx;
    inputs->pad_idx    = 0;
    inputs->next       = NULL;

    if (!outputs->name || !inputs->name) {
        ret = AVERROR(ENOMEM);
        goto end;
    }

    if ((ret = avfilter_graph_parse_ptr(filter_graph, filter_spec,
                    &inputs, &outputs, NULL)) < 0)
        goto end;

    if ((ret = avfilter_graph_config(filter_graph, NULL)) < 0)
        goto end;

    /* Fill FilteringContext */
    fctx->buffersrc_ctx = buffersrc_ctx;
    fctx->buffersink_ctx = buffersink_ctx;
    fctx->filter_graph = filter_graph;

end:
    avfilter_inout_free(&inputs);
    avfilter_inout_free(&outputs);

    return ret;
}

static int init_filters(void)
{
    const char *filter_spec;
    unsigned int i;
    int ret;
    filter_ctx = av_malloc_array(ifmt_ctx->nb_streams, sizeof(*filter_ctx));
    if (!filter_ctx)
        return AVERROR(ENOMEM);

    for (i = 0; i < ifmt_ctx->nb_streams; i++) {
        filter_ctx[i].buffersrc_ctx  = NULL;
        filter_ctx[i].buffersink_ctx = NULL;
        filter_ctx[i].filter_graph   = NULL;
        if (!(ifmt_ctx->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_AUDIO
                || ifmt_ctx->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO))
            continue;


        if (ifmt_ctx->streams[i]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO)
            filter_spec = "null"; /* passthrough (dummy) filter for video */
        else
            filter_spec = "anull"; /* passthrough (dummy) filter for audio */
        ret = init_filter(&filter_ctx[i], stream_ctx[i].dec_ctx,
                stream_ctx[i].enc_ctx, filter_spec);
        if (ret)
            return ret;
    }
    return 0;
}

static int encode_write_frame(AVFrame *filt_frame, unsigned int stream_index, int *got_frame) {
    int ret;
    int got_frame_local;
    AVPacket enc_pkt;

    if (!got_frame)
        got_frame = &got_frame_local;
    *got_frame = 0;

    av_log(NULL, AV_LOG_INFO, "Encoding frame\n");
    ret = avcodec_send_frame(stream_ctx[stream_index].enc_ctx, filt_frame);
    av_frame_free(&filt_frame);
    if (ret == AVERROR_EOF)
        return 0;
    if (ret < 0)
        return ret;

    while (1) {
        enc_pkt.data = NULL;
        enc_pkt.size = 0;
        av_init_packet(&enc_pkt);

        ret = avcodec_receive_packet(stream_ctx[stream_index].enc_ctx, &enc_pkt);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF)
            return 0;
        if (ret < 0)
            return ret;

        *got_frame = 1;

        /* prepare packet for muxing */
        enc_pkt.stream_index = stream_index;
        av_packet_rescale_ts(&enc_pkt,
                             stream_ctx[stream_index].enc_ctx->time_base,
                             ofmt_ctx->streams[stream_index]->time_base);

        av_log(NULL, AV_LOG_DEBUG, "Muxing frame\n");
        ret = av_interleaved_write_frame(ofmt_ctx, &enc_pkt);
        av_packet_unref(&enc_pkt);
        if (ret < 0)
            return ret;
    }

    return ret;
}

static int decode_filter_encode_write_frame(AVPacket *packet, unsigned int stream_index)
{
    AVFrame *frame;
    int ret;

    frame = av_frame_alloc();
    if (!frame)
        return AVERROR(ENOMEM);

    ret = avcodec_send_packet(stream_ctx[stream_index].dec_ctx, packet);
    if (ret < 0) {
        av_frame_free(&frame);
        av_log(NULL, AV_LOG_ERROR, "Error submitting a packet for decoding\n");
        return ret;
    }

    while (1) {
        ret = avcodec_receive_frame(stream_ctx[stream_index].dec_ctx, frame);
        if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF) {
            ret = 0;
            break;
        }
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Decoding failed\n");
            break;
        }

        frame->pts = frame->best_effort_timestamp;
        ret = filter_encode_write_frame(frame, stream_index);
        av_frame_unref(frame);
        if (ret < 0)
            break;
    }

    av_frame_free(&frame);
    return ret;
}

static int filter_encode_write_frame(AVFrame *frame, unsigned int stream_index)
{
    int ret;
    AVFrame *filt_frame;

    av_log(NULL, AV_LOG_INFO, "Pushing decoded frame to filters\n");
    /* push the decoded frame into the filtergraph */
    ret = av_buffersrc_add_frame_flags(filter_ctx[stream_index].buffersrc_ctx,
            frame, 0);
    if (ret < 0) {
        av_log(NULL, AV_LOG_ERROR, "Error while feeding the filtergraph\n");
        return ret;
    }

    /* pull filtered frames from the filtergraph */
    while (1) {
        filt_frame = av_frame_alloc();
        if (!filt_frame) {
            ret = AVERROR(ENOMEM);
            break;
        }
        av_log(NULL, AV_LOG_INFO, "Pulling filtered frame from filters\n");
        ret = av_buffersink_get_frame(filter_ctx[stream_index].buffersink_ctx,
                filt_frame);
        if (ret < 0) {
            /* if no more frames for output - returns AVERROR(EAGAIN)
             * if flushed and no more frames for output - returns AVERROR_EOF
             * rewrite retcode to 0 to show it as normal procedure completion
             */
            if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF)
                ret = 0;
            av_frame_free(&filt_frame);
            break;
        }

        filt_frame->pict_type = AV_PICTURE_TYPE_NONE;
#ifdef USE_UF_RENDERLIB
        if (ifmt_ctx->streams[stream_index]->codecpar->codec_type == AVMEDIA_TYPE_VIDEO &&
            !renderlib_ctx.render_disabled) {
            AVFrame *processed_frame = NULL;

            ret = process_video_frame_with_renderlib(filt_frame, stream_index,
                                                     &processed_frame);
            if (ret < 0) {
                if (renderlib_ctx.passthrough_on_failure) {
                    renderlib_ctx.render_disabled = 1;
                    av_log(NULL, AV_LOG_WARNING,
                           "uniqFEED render failed on stream #%u: %s; disabling uniqFEED and passing through original frames\n",
                           stream_index, av_err2str(ret));
                    ret = 0;
                } else {
                    av_frame_free(&filt_frame);
                    break;
                }
            }
            if (processed_frame) {
                av_frame_free(&filt_frame);
                filt_frame = processed_frame;
            }
        }
#endif
        ret = encode_write_frame(filt_frame, stream_index, NULL);
        if (ret < 0)
            break;
    }

    return ret;
}

static int flush_encoder(unsigned int stream_index)
{
    int ret;
    int got_frame;

    if (!(stream_ctx[stream_index].enc_ctx->codec->capabilities &
                AV_CODEC_CAP_DELAY))
        return 0;

    while (1) {
        av_log(NULL, AV_LOG_INFO, "Flushing stream #%u encoder\n", stream_index);
        ret = encode_write_frame(NULL, stream_index, &got_frame);
        if (ret < 0)
            break;
        if (!got_frame)
            return 0;
    }
    return ret;
}

int main(int argc, char **argv)
{
    int ret;
    AVPacket packet = { .data = NULL, .size = 0 };
    unsigned int stream_index;
    unsigned int i;

    if (
#ifdef USE_UF_RENDERLIB
    argc != 5
#else
        argc != 3
#endif
    ) {
#ifdef USE_UF_RENDERLIB
        av_log(NULL, AV_LOG_ERROR,
           "Usage: %s <input file> <output file> <project path> <metadata dir>\n",
               argv[0]);
#else
        av_log(NULL, AV_LOG_ERROR, "Usage: %s <input file> <output file>\n", argv[0]);
#endif
        return 1;
    }

    if ((ret = open_input_file(argv[1])) < 0)
        goto end;
#ifdef USE_UF_RENDERLIB
    if ((ret = init_renderlib(argc, argv)) < 0)
        goto end;
#endif
    if ((ret = open_output_file(argv[2])) < 0)
        goto end;
    if ((ret = init_filters()) < 0)
        goto end;

    /* read all packets */
    while (1) {
        if ((ret = av_read_frame(ifmt_ctx, &packet)) < 0) {
            if (ret == AVERROR_EOF) {
                ret = 0;
                break;
            }
            goto end;
        }
        stream_index = packet.stream_index;
        av_log(NULL, AV_LOG_DEBUG, "Demuxer gave frame of stream_index %u\n",
                stream_index);

        if (filter_ctx[stream_index].filter_graph) {
            av_log(NULL, AV_LOG_DEBUG, "Going to reencode&filter the frame\n");
            av_packet_rescale_ts(&packet,
                                 ifmt_ctx->streams[stream_index]->time_base,
                                 stream_ctx[stream_index].dec_ctx->time_base);
            ret = decode_filter_encode_write_frame(&packet, stream_index);
            if (ret < 0)
                goto end;
        } else {
            /* remux this frame without reencoding */
            av_packet_rescale_ts(&packet,
                                 ifmt_ctx->streams[stream_index]->time_base,
                                 ofmt_ctx->streams[stream_index]->time_base);

            ret = av_interleaved_write_frame(ofmt_ctx, &packet);
            if (ret < 0)
                goto end;
        }
        av_packet_unref(&packet);
    }

    for (i = 0; i < ifmt_ctx->nb_streams; i++) {
        if (!filter_ctx[i].filter_graph)
            continue;

        ret = decode_filter_encode_write_frame(NULL, i);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Flushing decoder failed\n");
            goto end;
        }
    }

    /* flush filters and encoders */
    for (i = 0; i < ifmt_ctx->nb_streams; i++) {
        /* flush filter */
        if (!filter_ctx[i].filter_graph)
            continue;
        ret = filter_encode_write_frame(NULL, i);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Flushing filter failed\n");
            goto end;
        }

        /* flush encoder */
        ret = flush_encoder(i);
        if (ret < 0) {
            av_log(NULL, AV_LOG_ERROR, "Flushing encoder failed\n");
            goto end;
        }
    }

    ret = av_write_trailer(ofmt_ctx);
    if (ret < 0)
        goto end;
end:
    av_packet_unref(&packet);
    if (ifmt_ctx && stream_ctx) {
        for (i = 0; i < ifmt_ctx->nb_streams; i++) {
            avcodec_free_context(&stream_ctx[i].dec_ctx);
            if (ofmt_ctx && ofmt_ctx->nb_streams > i && ofmt_ctx->streams[i] && stream_ctx[i].enc_ctx)
                avcodec_free_context(&stream_ctx[i].enc_ctx);
            if (filter_ctx && filter_ctx[i].filter_graph)
                avfilter_graph_free(&filter_ctx[i].filter_graph);
        }
    }
    av_free(filter_ctx);
    av_free(stream_ctx);
    avformat_close_input(&ifmt_ctx);
    if (ofmt_ctx && !(ofmt_ctx->oformat->flags & AVFMT_NOFILE))
        avio_closep(&ofmt_ctx->pb);
    avformat_free_context(ofmt_ctx);
#ifdef USE_UF_RENDERLIB
    free_renderlib();
#endif

    if (ret < 0)
        av_log(NULL, AV_LOG_ERROR, "Error occurred: %s\n", av_err2str(ret));

    return ret ? 1 : 0;
}
