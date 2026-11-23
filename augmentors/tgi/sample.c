/*
 * Augment a whole video with the SmartFeed Augmentor, in process, the way avpipe would:
 * libavformat/libavcodec decode, convert to I420 BT.709, sfa_augment, encode with libx264 and
 * mux to MP4. No ffmpeg executable and no pipes.
 *
 *   ./sample --video in.mov --metadata <dir> --output out.mp4
 *            --creative LeftTop=a.png --creative RightBottom=b.png ...
 *            [--start N] [--count N] [--crf 16] [--preset medium] [--cuda-device -1]
 *
 * <dir> holds the producer's files, one pair per frame, named by the frame's index in the
 * file (presentation order, counting from 0):
 *   N.json  the per-frame metadata document, passed to the library unchanged
 *   N.raw   GRAY8 mask, processingWidth x processingHeight, tightly packed
 * A frame with no N.json is encoded unaugmented. One with no N.raw is passed with no mask,
 * which the library returns unaugmented.
 *
 * Creative names are matched against the placement names in the metadata ("LeftTop",
 * "MidTopRight", ...). PNGs are decoded here: the library does no image decoding.
 *
 * --start N decodes from the beginning and discards frames before N, so the frame index used
 * to find the metadata stays exact.
 *
 * Build (Linux, FFmpeg development libraries via pkg-config):
 *   cd augmentors/tgi
 *   cc -std=c11 -Wall -Wextra -O2 sample.c -o sample \
 *      $(pkg-config --cflags --libs libavformat libavcodec libavutil libswscale) \
 *      -L. -lsfa_node -Wl,-rpath,'$ORIGIN'
 *
 * Exit status is 0 when the whole range was processed and no frame failed.
 */

#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/error.h>
#include <libavutil/opt.h>
#include <libavutil/pixdesc.h>
#include <libswscale/swscale.h>

#include "smartfeed_augmentor.h"

#define MAX_CREATIVES 32

typedef struct {
    const char *video;
    const char *metadata_dir;
    const char *output;
    const char *preset;
    const char *crf;
    int cuda_device;
    int64_t start;
    int64_t count; /* -1: to the end */
    int n_creatives;
    const char *creative_name[MAX_CREATIVES];
    const char *creative_path[MAX_CREATIVES];
} Options;

typedef struct {
    uint8_t *bgra;
    int width;
    int height;
} Picture;

typedef struct {
    const Options *opt;
    sfa_node *node;

    /* Decode side */
    AVFormatContext *in;
    AVCodecContext *dec;
    int in_stream;

    /* Encode side */
    AVFormatContext *out;
    AVCodecContext *enc;
    AVStream *out_stream;
    AVPacket *pkt;

    /* The frame the library augments and the encoder consumes. */
    AVFrame *i420;
    struct SwsContext *to_i420;

    int64_t index;     /* index in the file of the next decoded frame */
    int64_t processed; /* frames encoded */

    /* Counters */
    int64_t no_metadata;
    int64_t no_mask;
    int64_t status_count[SFA_FRAME_DAMAGED + 1];
    int failed;
} Sample;

static int report_error(const char *operation, int error)
{
    char message[AV_ERROR_MAX_STRING_SIZE];
    av_strerror(error, message, sizeof(message));
    fprintf(stderr, "%s: %s\n", operation, message);
    return error;
}

static void on_log(int level, const char *message, void *user)
{
    static const char *names[] = {"debug", "info", "warn", "error"};
    (void)user;
    if (level >= SFA_LOG_INFO)
        fprintf(stderr, "[sfa %s] %s\n", names[level < 0 || level > 3 ? 1 : level], message);
}

static void usage(const char *argv0)
{
    fprintf(stderr,
            "usage: %s --video <file> --metadata <dir> --output <file.mp4>\n"
            "          --creative <Placement>=<png> [--creative ...]\n"
            "          [--start N] [--count N] [--crf 16] [--preset medium] [--cuda-device -1]\n",
            argv0);
}

static int parse_args(int argc, char **argv, Options *o)
{
    o->preset = "medium";
    o->crf = "16";
    o->cuda_device = -1;
    o->count = -1;
    for (int i = 1; i < argc; ++i) {
        const char *arg = argv[i];
        if (i + 1 >= argc) {
            fprintf(stderr, "%s needs a value\n", arg);
            return -1;
        }
        const char *value = argv[++i];
        if (!strcmp(arg, "--video"))
            o->video = value;
        else if (!strcmp(arg, "--metadata"))
            o->metadata_dir = value;
        else if (!strcmp(arg, "--output"))
            o->output = value;
        else if (!strcmp(arg, "--preset"))
            o->preset = value;
        else if (!strcmp(arg, "--crf"))
            o->crf = value;
        else if (!strcmp(arg, "--cuda-device"))
            o->cuda_device = atoi(value);
        else if (!strcmp(arg, "--start"))
            o->start = strtoll(value, NULL, 10);
        else if (!strcmp(arg, "--count"))
            o->count = strtoll(value, NULL, 10);
        else if (!strcmp(arg, "--creative")) {
            const char *eq = strchr(value, '=');
            if (!eq || eq == value || !eq[1]) {
                fprintf(stderr, "--creative wants <Placement>=<png>, got %s\n", value);
                return -1;
            }
            if (o->n_creatives == MAX_CREATIVES) {
                fprintf(stderr, "at most %d creatives\n", MAX_CREATIVES);
                return -1;
            }
            o->creative_name[o->n_creatives] = av_strndup(value, eq - value);
            o->creative_path[o->n_creatives] = eq + 1;
            ++o->n_creatives;
        } else {
            fprintf(stderr, "unknown argument %s\n", arg);
            return -1;
        }
    }
    if (!o->video || !o->metadata_dir || !o->output || o->n_creatives == 0 || o->start < 0)
        return -1;
    return 0;
}

/* Reads a whole file. Returns 0, or -1 if it cannot be opened or read. */
static int read_file(const char *path, char **data, size_t *size)
{
    FILE *file = fopen(path, "rb");
    long length;
    *data = NULL;
    *size = 0;
    if (!file)
        return -1;
    if (fseek(file, 0, SEEK_END) != 0 || (length = ftell(file)) < 0 ||
        fseek(file, 0, SEEK_SET) != 0) {
        fclose(file);
        return -1;
    }
    *data = malloc(length > 0 ? (size_t)length : 1);
    if (!*data || fread(*data, 1, (size_t)length, file) != (size_t)length) {
        free(*data);
        *data = NULL;
        fclose(file);
        return -1;
    }
    fclose(file);
    *size = (size_t)length;
    return 0;
}

/* The integer value of a top-level "key": n in the producer's document; 0 if absent. */
static int json_int(const char *json, size_t len, const char *key)
{
    char needle[64];
    const char *end = json + len;
    size_t needle_len;
    snprintf(needle, sizeof(needle), "\"%s\"", key);
    needle_len = strlen(needle);
    for (const char *p = json; p + needle_len <= end; ++p) {
        if (memcmp(p, needle, needle_len) != 0)
            continue;
        p += needle_len;
        while (p < end && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r'))
            ++p;
        if (p >= end || *p != ':')
            return 0;
        ++p;
        int value = 0;
        while (p < end && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r'))
            ++p;
        while (p < end && *p >= '0' && *p <= '9')
            value = value * 10 + (*p++ - '0');
        return value;
    }
    return 0;
}

/* Decode a PNG (or any image FFmpeg reads) into straight-alpha BGRA. */
static int load_picture(const char *path, Picture *picture)
{
    AVFormatContext *in = NULL;
    AVCodecContext *dec = NULL;
    const AVCodec *codec = NULL;
    AVPacket *pkt = av_packet_alloc();
    AVFrame *frame = av_frame_alloc();
    struct SwsContext *sws = NULL;
    int ret, stream;

    if (!pkt || !frame) {
        ret = AVERROR(ENOMEM);
        goto done;
    }
    if ((ret = avformat_open_input(&in, path, NULL, NULL)) < 0 ||
        (ret = avformat_find_stream_info(in, NULL)) < 0) {
        report_error(path, ret);
        goto done;
    }
    if ((stream = ret = av_find_best_stream(in, AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0)) < 0) {
        report_error(path, ret);
        goto done;
    }
    if (!(dec = avcodec_alloc_context3(codec))) {
        ret = AVERROR(ENOMEM);
        goto done;
    }
    if ((ret = avcodec_parameters_to_context(dec, in->streams[stream]->codecpar)) < 0 ||
        (ret = avcodec_open2(dec, codec, NULL)) < 0) {
        report_error(path, ret);
        goto done;
    }
    ret = AVERROR(EAGAIN);
    while (ret == AVERROR(EAGAIN)) {
        int read = av_read_frame(in, pkt);
        if (read >= 0 && pkt->stream_index != stream) {
            av_packet_unref(pkt);
            continue;
        }
        avcodec_send_packet(dec, read >= 0 ? pkt : NULL);
        av_packet_unref(pkt);
        ret = avcodec_receive_frame(dec, frame);
        if (read < 0 && ret == AVERROR(EAGAIN))
            ret = AVERROR_EOF;
    }
    if (ret < 0) {
        report_error(path, ret);
        goto done;
    }

    picture->width = frame->width;
    picture->height = frame->height;
    picture->bgra = av_malloc((size_t)frame->width * frame->height * 4);
    sws = sws_getContext(frame->width, frame->height, frame->format, frame->width, frame->height,
                         AV_PIX_FMT_BGRA, SWS_POINT, NULL, NULL, NULL);
    if (!picture->bgra || !sws) {
        ret = AVERROR(ENOMEM);
        goto done;
    }
    uint8_t *dst[4] = {picture->bgra};
    int dst_stride[4] = {frame->width * 4};
    ret = sws_scale(sws, (const uint8_t *const *)frame->data, frame->linesize, 0, frame->height,
                    dst, dst_stride);
    ret = ret == frame->height ? 0 : AVERROR(EINVAL);

done:
    sws_freeContext(sws);
    av_frame_free(&frame);
    av_packet_free(&pkt);
    avcodec_free_context(&dec);
    avformat_close_input(&in);
    return ret;
}

static int open_input(Sample *s)
{
    const AVCodec *codec = NULL;
    int ret;

    if ((ret = avformat_open_input(&s->in, s->opt->video, NULL, NULL)) < 0 ||
        (ret = avformat_find_stream_info(s->in, NULL)) < 0)
        return report_error("Open video", ret);
    if ((s->in_stream = av_find_best_stream(s->in, AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0)) < 0)
        return report_error("Find video stream", s->in_stream);
    if (!(s->dec = avcodec_alloc_context3(codec)))
        return AVERROR(ENOMEM);
    s->dec->thread_count = 0;
    if ((ret = avcodec_parameters_to_context(s->dec, s->in->streams[s->in_stream]->codecpar)) < 0 ||
        (ret = avcodec_open2(s->dec, codec, NULL)) < 0)
        return report_error("Open video decoder", ret);
    if (s->dec->width <= 0 || s->dec->height <= 0 || s->dec->width % 2 || s->dec->height % 2) {
        fprintf(stderr, "I420 augmentation requires positive, even video dimensions\n");
        return AVERROR(EINVAL);
    }
    return 0;
}

static int open_output(Sample *s, AVRational fps)
{
    const AVCodec *codec = avcodec_find_encoder_by_name("libx264");
    int ret;

    if (!codec) {
        fprintf(stderr, "This FFmpeg has no libx264 encoder\n");
        return AVERROR_ENCODER_NOT_FOUND;
    }
    if ((ret = avformat_alloc_output_context2(&s->out, NULL, NULL, s->opt->output)) < 0)
        return report_error("Create output", ret);
    if (!(s->enc = avcodec_alloc_context3(codec)) ||
        !(s->out_stream = avformat_new_stream(s->out, NULL)))
        return AVERROR(ENOMEM);

    s->enc->width = s->dec->width;
    s->enc->height = s->dec->height;
    s->enc->pix_fmt = AV_PIX_FMT_YUV420P;
    s->enc->framerate = fps;
    s->enc->time_base = av_inv_q(fps);
    s->enc->sample_aspect_ratio = s->dec->sample_aspect_ratio;
    /* What the augmentor produces: limited-range BT.709. */
    s->enc->colorspace = AVCOL_SPC_BT709;
    s->enc->color_primaries = AVCOL_PRI_BT709;
    s->enc->color_trc = AVCOL_TRC_BT709;
    s->enc->color_range = AVCOL_RANGE_MPEG;
    s->enc->thread_count = 0;
    if (s->out->oformat->flags & AVFMT_GLOBALHEADER)
        s->enc->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;
    av_opt_set(s->enc->priv_data, "preset", s->opt->preset, 0);
    av_opt_set(s->enc->priv_data, "crf", s->opt->crf, 0);

    if ((ret = avcodec_open2(s->enc, codec, NULL)) < 0)
        return report_error("Open encoder", ret);
    if ((ret = avcodec_parameters_from_context(s->out_stream->codecpar, s->enc)) < 0)
        return report_error("Copy encoder parameters", ret);
    s->out_stream->time_base = s->enc->time_base;
    s->out_stream->avg_frame_rate = fps;
    if (!(s->out->oformat->flags & AVFMT_NOFILE) &&
        (ret = avio_open(&s->out->pb, s->opt->output, AVIO_FLAG_WRITE)) < 0)
        return report_error(s->opt->output, ret);
    if ((ret = avformat_write_header(s->out, NULL)) < 0)
        return report_error("Write output header", ret);

    s->i420 = av_frame_alloc();
    if (!s->i420)
        return AVERROR(ENOMEM);
    s->i420->format = AV_PIX_FMT_YUV420P;
    s->i420->width = s->dec->width;
    s->i420->height = s->dec->height;
    if ((ret = av_frame_get_buffer(s->i420, 32)) < 0)
        return report_error("Allocate I420 frame", ret);
    return 0;
}

/* Send one frame (or NULL to flush) and mux whatever the encoder hands back. */
static int encode(Sample *s, AVFrame *frame)
{
    int ret = avcodec_send_frame(s->enc, frame);
    if (ret < 0)
        return report_error("Encode", ret);
    while ((ret = avcodec_receive_packet(s->enc, s->pkt)) >= 0) {
        av_packet_rescale_ts(s->pkt, s->enc->time_base, s->out_stream->time_base);
        s->pkt->stream_index = s->out_stream->index;
        if ((ret = av_interleaved_write_frame(s->out, s->pkt)) < 0)
            return report_error("Write packet", ret);
    }
    return ret == AVERROR(EAGAIN) || ret == AVERROR_EOF ? 0 : report_error("Encode", ret);
}

/* Decoded frame (any format) -> s->i420, limited-range BT.709 as the library expects. */
static int convert(Sample *s, const AVFrame *decoded)
{
    int ret;
    s->to_i420 = sws_getCachedContext(s->to_i420, decoded->width, decoded->height,
                                      decoded->format, decoded->width, decoded->height,
                                      AV_PIX_FMT_YUV420P, SWS_BILINEAR, NULL, NULL, NULL);
    if (!s->to_i420)
        return report_error("Create I420 converter", AVERROR(ENOMEM));
    /* Honor tagged source color/range; untagged HD is taken as BT.709. */
    int space = decoded->colorspace;
    if (space != AVCOL_SPC_BT709 && space != AVCOL_SPC_FCC && space != AVCOL_SPC_BT470BG &&
        space != AVCOL_SPC_SMPTE170M && space != AVCOL_SPC_SMPTE240M &&
        space != AVCOL_SPC_BT2020_NCL)
        space = decoded->height >= 720 ? SWS_CS_ITU709 : SWS_CS_ITU601;
    if ((ret = sws_setColorspaceDetails(s->to_i420, sws_getCoefficients(space),
                                        decoded->color_range == AVCOL_RANGE_JPEG,
                                        sws_getCoefficients(SWS_CS_ITU709), 0, 0, 1 << 16,
                                        1 << 16)) < 0)
        return report_error("Set I420 color conversion", ret);
    ret = sws_scale(s->to_i420, (const uint8_t *const *)decoded->data, decoded->linesize, 0,
                    decoded->height, s->i420->data, s->i420->linesize);
    return ret == decoded->height ? 0 : report_error("Convert to I420", ret < 0 ? ret : AVERROR(EINVAL));
}

/* Pair the frame with N.json / N.raw and augment s->i420 in place. */
static int augment(Sample *s, int64_t index, const AVFrame *decoded)
{
    char path[4096];
    char *json = NULL, *mask = NULL;
    size_t json_len = 0, mask_len = 0;
    sfa_frame_metadata metadata = {0};
    sfa_image image = {0};
    int ret = 0;

    snprintf(path, sizeof(path), "%s/%" PRId64 ".json", s->opt->metadata_dir, index);
    if (read_file(path, &json, &json_len) < 0) {
        ++s->no_metadata;
        return 0;
    }
    metadata.json = json;
    metadata.json_len = (int32_t)json_len;

    snprintf(path, sizeof(path), "%s/%" PRId64 ".raw", s->opt->metadata_dir, index);
    if (read_file(path, &mask, &mask_len) == 0) {
        int w = json_int(json, json_len, "processingWidth");
        int h = json_int(json, json_len, "processingHeight");
        if (w > 0 && h > 0 && mask_len == (size_t)w * h) {
            metadata.mask = (const uint8_t *)mask;
            metadata.mask_stride = w;
            metadata.mask_width = w;
            metadata.mask_height = h;
        } else {
            fprintf(stderr, "frame %" PRId64 ": mask is %zu bytes for a %dx%d processing space; passing none\n",
                    index, mask_len, w, h);
            ++s->no_mask;
        }
    } else {
        ++s->no_mask;
    }

    image.width = s->i420->width;
    image.height = s->i420->height;
    for (int i = 0; i < 3; ++i) {
        image.plane[i] = s->i420->data[i];
        image.stride[i] = s->i420->linesize[i];
    }

    sfa_status status = sfa_augment(s->node, &image, &metadata);
    if (status >= 0 && status <= SFA_FRAME_DAMAGED)
        ++s->status_count[status];
    if (status != SFA_OK && status != SFA_UNMODIFIED) {
        fprintf(stderr, "frame %" PRId64 ": status %d: %s\n", index, (int)status,
                sfa_last_error(s->node));
        s->failed = 1;
        /* A damaged frame is neither source nor augmented. Never encode it: rebuild the
         * clean frame from the decoder's output, which the library never touched. */
        if (status == SFA_FRAME_DAMAGED)
            ret = convert(s, decoded);
    }
    free(json);
    free(mask);
    return ret;
}

static int done_with_range(const Sample *s)
{
    return s->opt->count >= 0 && s->processed >= s->opt->count;
}

static int process_frame(Sample *s, const AVFrame *decoded)
{
    int64_t index = s->index++;
    int ret;

    if (index < s->opt->start || done_with_range(s))
        return 0;
    if (decoded->width != s->i420->width || decoded->height != s->i420->height) {
        fprintf(stderr, "Video dimensions changed during decoding\n");
        return AVERROR(EINVAL);
    }
    /* The encoder may still hold a reference to the previous frame's buffers. */
    if ((ret = av_frame_make_writable(s->i420)) < 0)
        return report_error("Make frame writable", ret);
    if ((ret = convert(s, decoded)) < 0 || (ret = augment(s, index, decoded)) < 0)
        return ret;

    s->i420->pts = s->processed;
    if ((ret = encode(s, s->i420)) < 0)
        return ret;
    ++s->processed;
    if (s->processed % 600 == 0)
        printf("frame %" PRId64 " (%" PRId64 " done)\n", index, s->processed);
    return 0;
}

/* Drain after every packet and once more after sending NULL at end of input. */
static int receive_frames(Sample *s, AVFrame *frame)
{
    int ret;
    while ((ret = avcodec_receive_frame(s->dec, frame)) >= 0) {
        ret = process_frame(s, frame);
        av_frame_unref(frame);
        if (ret < 0)
            return ret;
    }
    if (ret == AVERROR(EAGAIN) || ret == AVERROR_EOF)
        return 0;
    return report_error("Decode frame", ret);
}

int main(int argc, char **argv)
{
    Options opt = {0};
    Sample s = {0};
    Picture pictures[MAX_CREATIVES] = {{0}};
    sfa_creative creatives[MAX_CREATIVES] = {{0}};
    sfa_init_params params = {0};
    AVPacket *packet = NULL;
    AVFrame *decoded = NULL;
    AVRational fps;
    int ret = 0, exit_code = 1;

    if (parse_args(argc, argv, &opt) < 0) {
        usage(argv[0]);
        return 2;
    }
    s.opt = &opt;
    if (sfa_abi_version() != SFA_ABI_VERSION) {
        fprintf(stderr, "Augmentor ABI mismatch: header %u, library %u\n",
                (unsigned)SFA_ABI_VERSION, sfa_abi_version());
        return 1;
    }
    printf("%s\n", sfa_build_info());

    /* ---- creatives ---- */
    for (int i = 0; i < opt.n_creatives; ++i) {
        if (load_picture(opt.creative_path[i], &pictures[i]) < 0)
            goto done;
        creatives[i].name = opt.creative_name[i];
        creatives[i].bgra = pictures[i].bgra;
        creatives[i].stride = pictures[i].width * 4;
        creatives[i].width = pictures[i].width;
        creatives[i].height = pictures[i].height;
        printf("creative %s <- %s (%dx%d)\n", opt.creative_name[i], opt.creative_path[i],
               pictures[i].width, pictures[i].height);
    }

    /* ---- input, output ---- */
    if (open_input(&s) < 0)
        goto done;
    fps = av_guess_frame_rate(s.in, s.in->streams[s.in_stream], NULL);
    if (fps.num <= 0 || fps.den <= 0) {
        fprintf(stderr, "Cannot determine the frame rate of %s\n", opt.video);
        goto done;
    }
    printf("input %dx%d %s @ %d/%d fps\n", s.dec->width, s.dec->height,
           av_get_pix_fmt_name(s.dec->pix_fmt), fps.num, fps.den);
    if (!(packet = av_packet_alloc()) || !(decoded = av_frame_alloc()) ||
        !(s.pkt = av_packet_alloc())) {
        fprintf(stderr, "Could not allocate packets or frames\n");
        goto done;
    }
    if (open_output(&s, fps) < 0)
        goto done;

    /* ---- augmentor ---- */
    params.cuda_device = opt.cuda_device;
    params.fps = av_q2d(fps);
    params.creatives = creatives;
    params.n_creatives = opt.n_creatives;
    params.log_cb = on_log;
    sfa_status status = sfa_init(&params, &s.node);
    if (status != SFA_OK) {
        fprintf(stderr, "sfa_init failed: %d\n", (int)status);
        goto done;
    }
    /* The library holds its own copies now. */
    for (int i = 0; i < opt.n_creatives; ++i)
        av_freep(&pictures[i].bgra);

    /* ---- decode -> augment -> encode ---- */
    while (!done_with_range(&s) && (ret = av_read_frame(s.in, packet)) >= 0) {
        if (packet->stream_index == s.in_stream) {
            if ((ret = avcodec_send_packet(s.dec, packet)) < 0) {
                report_error("Send video packet", ret);
                goto done;
            }
            if (receive_frames(&s, decoded) < 0)
                goto done;
        }
        av_packet_unref(packet);
    }
    if (!done_with_range(&s)) {
        if (ret != AVERROR_EOF) {
            report_error("Read video packet", ret);
            goto done;
        }
        if ((ret = avcodec_send_packet(s.dec, NULL)) < 0) {
            report_error("Flush video decoder", ret);
            goto done;
        }
        if (receive_frames(&s, decoded) < 0)
            goto done;
    }
    if (encode(&s, NULL) < 0)
        goto done;
    if ((ret = av_write_trailer(s.out)) < 0) {
        report_error("Write output trailer", ret);
        goto done;
    }

    sfa_stats stats = {0};
    sfa_get_stats(s.node, &stats);
    printf("frames written:        %" PRId64 "\n"
           "  no metadata:         %" PRId64 "\n"
           "  no mask:             %" PRId64 "\n"
           "  SFA_OK:              %" PRId64 "\n"
           "  SFA_UNMODIFIED:      %" PRId64 "\n"
           "  damaged (re-encoded clean): %" PRId64 "\n"
           "  failed:              %" PRIu64 "\n"
           "placements drawn:      %" PRIu64 "\n"
           "skipped, no creative:  %" PRIu64 "\n"
           "skipped, no mask:      %" PRIu64 "\n"
           "not live:              %" PRIu64 "\n"
           "fully occluded frames: %" PRIu64 "\n"
           "augment time mean/max: %" PRIu64 " / %" PRIu64 " us\n",
           s.processed, s.no_metadata, s.no_mask, s.status_count[SFA_OK],
           s.status_count[SFA_UNMODIFIED], s.status_count[SFA_FRAME_DAMAGED],
           stats.frames_failed, stats.augmented_placements, stats.skipped_no_creative,
           stats.skipped_no_mask, stats.not_live, stats.frames_fully_occluded,
           stats.frames_augmented ? stats.augment_time_us_total / stats.frames_augmented : 0,
           stats.augment_time_us_max);

    if (s.processed == 0)
        fprintf(stderr, "No frames were decoded from %s\n", opt.video);
    else
        exit_code = s.failed;

done:
    sfa_destroy(s.node);
    for (int i = 0; i < opt.n_creatives; ++i) {
        av_freep(&pictures[i].bgra);
        av_free((void *)opt.creative_name[i]);
    }
    sws_freeContext(s.to_i420);
    av_frame_free(&s.i420);
    av_frame_free(&decoded);
    av_packet_free(&packet);
    av_packet_free(&s.pkt);
    avcodec_free_context(&s.enc);
    avcodec_free_context(&s.dec);
    if (s.out && !(s.out->oformat->flags & AVFMT_NOFILE))
        avio_closep(&s.out->pb);
    avformat_free_context(s.out);
    avformat_close_input(&s.in);
    return exit_code;
}
