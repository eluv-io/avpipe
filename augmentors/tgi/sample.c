/*
 * Decode a local video, augment each frame, and save frame_000001.png, etc.
 * Uses the synthetic CourtSide placement, white mask, and blue advert from
 * augment_one_frame.c; these are demonstration inputs, not scene tracking.
 *
 * Build on Linux with FFmpeg development libraries and pkg-config installed:
 *   cd augmentors/tgi
 *   cc -std=c11 -Wall -Wextra -O2 sample.c -o sample \
 *      $(pkg-config --cflags --libs libavformat libavcodec libavutil libswscale) \
 *      -L. -lsfa_node -Wl,-rpath,'$ORIGIN'
 * Run on a machine supported by libsfa_node.so (Linux x86-64, NVIDIA GPU):
 *   ./sample /path/to/test.mp4 /path/to/new-output-directory
 * The output directory must not already exist, to avoid overwriting images.
 */

#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/error.h>
#include <libswscale/swscale.h>

#include "smartfeed_augmentor.h"

#define PROCESSING_WIDTH 1280
#define PROCESSING_HEIGHT 720

static const char metadata_json[] =
    "{\"processingWidth\":1280,\"processingHeight\":720,"
    "\"isLive\":true,\"period\":\"1\",\"placeholders\":{\"group-a\":{"
    "\"name\":\"CourtSide\",\"corners\":[{\"x\":400,\"y\":300},"
    "{\"x\":880,\"y\":300},{\"x\":880,\"y\":480},{\"x\":400,\"y\":480}]}}}";

typedef struct {
    sfa_node *node;
    sfa_frame_metadata metadata;
    AVCodecContext *png;
    AVFrame *i420;
    AVFrame *rgb;
    AVPacket *image;
    struct SwsContext *to_i420;
    struct SwsContext *to_rgb;
    const char *output_dir;
    uint64_t written;
    uint64_t unmodified;
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
    (void)level;
    (void)user;
    fprintf(stderr, "[augmentor] %s\n", message);
}

static int allocate_frame(AVFrame *frame, enum AVPixelFormat format, int w, int h)
{
    frame->format = format;
    frame->width = w;
    frame->height = h;
    return av_frame_get_buffer(frame, 32);
}

static int write_frame(Sample *sample, const AVFrame *decoded)
{
    sfa_image frame = {0};
    sfa_status status;
    FILE *file;
    char *path;
    size_t path_size;
    int ret;

    /* A single node and PNG encoder are configured for a fixed frame size. */
    if (decoded->width != sample->i420->width ||
        decoded->height != sample->i420->height) {
        fprintf(stderr, "Video dimensions changed during decoding\n");
        return AVERROR(EINVAL);
    }
    if ((ret = av_frame_make_writable(sample->i420)) < 0 ||
        (ret = av_frame_make_writable(sample->rgb)) < 0)
        return report_error("Make frame writable", ret);

    sample->to_i420 = sws_getCachedContext(sample->to_i420,
        decoded->width, decoded->height, decoded->format,
        decoded->width, decoded->height, AV_PIX_FMT_YUV420P,
        SWS_BILINEAR, NULL, NULL, NULL);
    if (!sample->to_i420)
        return report_error("Create I420 converter", AVERROR(ENOMEM));
    /* Honor tagged source color/range when converting to the library's BT.709. */
    int source_space = decoded->colorspace;
    if (source_space != AVCOL_SPC_BT709 && source_space != AVCOL_SPC_FCC &&
        source_space != AVCOL_SPC_BT470BG && source_space != AVCOL_SPC_SMPTE170M &&
        source_space != AVCOL_SPC_SMPTE240M && source_space != AVCOL_SPC_BT2020_NCL)
        source_space = decoded->height >= 720 ? SWS_CS_ITU709 : SWS_CS_ITU601;
    ret = sws_setColorspaceDetails(sample->to_i420, sws_getCoefficients(source_space),
        decoded->color_range == AVCOL_RANGE_JPEG,
        sws_getCoefficients(SWS_CS_ITU709), 0, 0, 1 << 16, 1 << 16);
    if (ret < 0)
        return report_error("Set I420 color conversion", ret);
    ret = sws_scale(sample->to_i420, (const uint8_t *const *)decoded->data,
                    decoded->linesize, 0, decoded->height,
                    sample->i420->data, sample->i420->linesize);
    if (ret != decoded->height)
        return report_error("Convert to I420", ret < 0 ? ret : AVERROR(EINVAL));

    frame.width = decoded->width;
    frame.height = decoded->height;
    for (int i = 0; i < 3; ++i) {
        frame.plane[i] = sample->i420->data[i];
        frame.stride[i] = sample->i420->linesize[i];
    }
    status = sfa_augment(sample->node, &frame, &sample->metadata);
    if (status != SFA_OK && status != SFA_UNMODIFIED) {
        /* Never encode a damaged frame. Stop on other augmentation failures too. */
        fprintf(stderr, "sfa_augment failed on frame %" PRIu64 " (%d): %s\n",
                sample->written + 1, (int)status, sfa_last_error(sample->node));
        return AVERROR_EXTERNAL;
    }
    if (status == SFA_UNMODIFIED)
        ++sample->unmodified;

    ret = sws_scale(sample->to_rgb, (const uint8_t *const *)sample->i420->data,
                    sample->i420->linesize, 0, frame.height,
                    sample->rgb->data, sample->rgb->linesize);
    if (ret != frame.height)
        return report_error("Convert to RGB", ret < 0 ? ret : AVERROR(EINVAL));
    sample->rgb->pts = (int64_t)sample->written;
    if ((ret = avcodec_send_frame(sample->png, sample->rgb)) < 0 ||
        (ret = avcodec_receive_packet(sample->png, sample->image)) < 0)
        return report_error("Encode PNG", ret);

    path_size = strlen(sample->output_dir) + 64;
    path = malloc(path_size);
    if (!path)
        return report_error("Allocate output path", AVERROR(ENOMEM));
    snprintf(path, path_size, "%s/frame_%06" PRIu64 ".png",
             sample->output_dir, sample->written + 1);
    file = fopen(path, "wb");
    if (!file) {
        ret = AVERROR(errno);
        report_error(path, ret);
        free(path);
        return ret;
    }
    ret = 0;
    if (fwrite(sample->image->data, 1, sample->image->size, file) !=
        (size_t)sample->image->size)
        ret = AVERROR(EIO);
    if (fclose(file) != 0 && ret == 0)
        ret = AVERROR(errno);
    if (ret < 0) {
        report_error(path, ret);
        remove(path);
    }
    free(path);
    av_packet_unref(sample->image);
    if (ret == 0)
        ++sample->written;
    return ret;
}

/* Drain after every packet and once more after sending NULL at end of input.
 * This includes delayed frames (for example, the final B-frames of an MP4).
 */
static int receive_frames(Sample *sample, AVCodecContext *decoder, AVFrame *frame)
{
    int ret;
    while ((ret = avcodec_receive_frame(decoder, frame)) >= 0) {
        ret = write_frame(sample, frame);
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
    Sample sample = {0};
    AVFormatContext *input = NULL;
    AVCodecContext *decoder = NULL;
    const AVCodec *codec = NULL;
    AVPacket *packet = NULL;
    AVFrame *decoded = NULL;
    uint8_t *mask = NULL;
    uint8_t advert[256 * 128 * 4];
    sfa_creative creative = {0};
    sfa_init_params params = {0};
    sfa_status status;
    AVRational fps;
    int stream, ret, exit_code = 1;

    if (argc != 3) {
        fprintf(stderr, "Usage: %s input.mp4 new-output-directory\n", argv[0]);
        return 1;
    }
    if (sfa_abi_version() != SFA_ABI_VERSION) {
        fprintf(stderr, "Augmentor ABI mismatch: header %u, library %u\n",
                (unsigned)SFA_ABI_VERSION, sfa_abi_version());
        return 1;
    }
    printf("%s\n", sfa_build_info());
    sample.output_dir = argv[2];
    if ((ret = avformat_open_input(&input, argv[1], NULL, NULL)) < 0 ||
        (ret = avformat_find_stream_info(input, NULL)) < 0) {
        report_error("Open video", ret);
        goto done;
    }
    stream = av_find_best_stream(input, AVMEDIA_TYPE_VIDEO, -1, -1, &codec, 0);
    if (stream < 0) {
        report_error("Find video stream", stream);
        goto done;
    }
    decoder = avcodec_alloc_context3(codec);
    sample.png = avcodec_alloc_context3(avcodec_find_encoder(AV_CODEC_ID_PNG));
    decoded = av_frame_alloc();
    sample.i420 = av_frame_alloc();
    sample.rgb = av_frame_alloc();
    packet = av_packet_alloc();
    sample.image = av_packet_alloc();
    mask = malloc((size_t)PROCESSING_WIDTH * PROCESSING_HEIGHT);
    if (!decoder || !sample.png || !decoded || !sample.i420 || !sample.rgb ||
        !packet || !sample.image || !mask) {
        fprintf(stderr, "Could not allocate frames, codecs, packets, or mask\n");
        goto done;
    }
    if ((ret = avcodec_parameters_to_context(decoder, input->streams[stream]->codecpar)) < 0 ||
        (ret = avcodec_open2(decoder, codec, NULL)) < 0) {
        report_error("Open video decoder", ret);
        goto done;
    }
    if (decoder->width <= 0 || decoder->height <= 0 ||
        decoder->width % 2 || decoder->height % 2) {
        fprintf(stderr, "I420 augmentation requires positive, even video dimensions\n");
        goto done;
    }
    sample.png->width = decoder->width;
    sample.png->height = decoder->height;
    sample.png->pix_fmt = AV_PIX_FMT_RGB24;
    sample.png->time_base = (AVRational){1, 1};
    if ((ret = avcodec_open2(sample.png, NULL, NULL)) < 0 ||
        (ret = allocate_frame(sample.i420, AV_PIX_FMT_YUV420P, decoder->width, decoder->height)) < 0 ||
        (ret = allocate_frame(sample.rgb, AV_PIX_FMT_RGB24, decoder->width, decoder->height)) < 0) {
        report_error("Set up PNG output", ret);
        goto done;
    }
    sample.to_rgb = sws_getContext(decoder->width, decoder->height, AV_PIX_FMT_YUV420P,
        decoder->width, decoder->height, AV_PIX_FMT_RGB24, SWS_BILINEAR, NULL, NULL, NULL);
    if (!sample.to_rgb)
        goto done;
    /* The augmentor uses limited-range BT.709 I420. */
    ret = sws_setColorspaceDetails(sample.to_rgb, sws_getCoefficients(SWS_CS_ITU709), 0,
        sws_getCoefficients(SWS_CS_ITU709), 1, 0, 1 << 16, 1 << 16);
    if (ret < 0) {
        report_error("Set PNG color conversion", ret);
        goto done;
    }
    memset(mask, 255, (size_t)PROCESSING_WIDTH * PROCESSING_HEIGHT);
    for (size_t i = 0; i < sizeof(advert); i += 4) {
        advert[i] = 220;
        advert[i + 1] = 90;
        advert[i + 2] = 30;
        advert[i + 3] = 255;
    }
    creative.name = "CourtSide";
    creative.bgra = advert;
    creative.width = 256;
    creative.height = 128;
    creative.stride = 256 * 4;
    fps = av_guess_frame_rate(input, input->streams[stream], NULL);
    params.fps = fps.num > 0 && fps.den > 0 ? av_q2d(fps) : 30.0;
    params.cuda_device = -1;
    params.creatives = &creative;
    params.n_creatives = 1;
    params.log_cb = on_log;
    status = sfa_init(&params, &sample.node);
    if (status != SFA_OK) {
        fprintf(stderr, "sfa_init failed: %d\n", (int)status);
        goto done;
    }
    sample.metadata.json = metadata_json;
    sample.metadata.json_len = (int32_t)strlen(metadata_json);
    sample.metadata.mask = mask;
    sample.metadata.mask_stride = PROCESSING_WIDTH;
    sample.metadata.mask_width = PROCESSING_WIDTH;
    sample.metadata.mask_height = PROCESSING_HEIGHT;
    if (mkdir(sample.output_dir, 0755) != 0) {
        report_error("Create output directory (must be new; parent must exist)", AVERROR(errno));
        goto done;
    }
    while ((ret = av_read_frame(input, packet)) >= 0) {
        if (packet->stream_index == stream) {
            ret = avcodec_send_packet(decoder, packet);
            if (ret < 0) {
                report_error("Send video packet", ret);
                goto done;
            }
            if (receive_frames(&sample, decoder, decoded) < 0)
                goto done;
        }
        av_packet_unref(packet);
    }
    if (ret != AVERROR_EOF) {
        report_error("Read video packet", ret);
        goto done;
    }
    if ((ret = avcodec_send_packet(decoder, NULL)) < 0) {
        report_error("Flush video decoder", ret);
        goto done;
    }
    if (receive_frames(&sample, decoder, decoded) < 0)
        goto done;
    if (sample.written == 0) {
        fprintf(stderr, "No video frames decoded\n");
        goto done;
    }
    printf("Wrote %" PRIu64 " PNG frames to %s (%" PRIu64 " unmodified)\n",
           sample.written, sample.output_dir, sample.unmodified);
    exit_code = 0;

done:
    sfa_destroy(sample.node);
    free(mask);
    sws_freeContext(sample.to_i420);
    sws_freeContext(sample.to_rgb);
    av_packet_free(&sample.image);
    av_packet_free(&packet);
    av_frame_free(&sample.i420);
    av_frame_free(&sample.rgb);
    av_frame_free(&decoded);
    avcodec_free_context(&sample.png);
    avcodec_free_context(&decoder);
    avformat_close_input(&input);
    return exit_code;
}
