/*
 * Unit tests and generated MPEGTS tests for PTS unwrap.
 */

#include "unity/unity.h"

#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/error.h>
#include <libavutil/frame.h>
#include <libavutil/opt.h>

#include "../src/avpipe_format.c"

#define MPEGTS_WRAP_MODULUS ((int64_t)1 << 33)
#define VIDEO_TIME_BASE     ((AVRational){1, 25})
#define MPEGTS_TIME_BASE    ((AVRational){1, 90000})
#define FRAME_DURATION_TS   3600
#define GENERATED_FRAMES    24
#define MAX_CAPTURED_PTS    64

typedef struct unwrap_result_t {
    int rc;
    int packet_count;
    int missing_timestamps;
    int pts_reorders;
    int pts_forward_wraps;
    int pts_backward_wraps;
    int dts_forward_wraps;
    int dts_backward_wraps;
    int nonmonotonic_dts;
    int dts_duration_errors;
    int invalid_pts_dts;
    int presentation_gaps;
} unwrap_result_t;

static const AVInputFormat test_mpegts_input_format = {
    .name = "mpegts",
};

static const AVInputFormat test_non_mpegts_input_format = {
    .name = "matroska,webm",
};

void setUp(void)    {}
void tearDown(void) {}

static int
make_unwrap_context(
    coderctx_t *ctx,
    const AVInputFormat *input_format,
    int pts_wrap_bits,
    AVRational time_base)
{
    AVStream *stream;

    memset(ctx, 0, sizeof(*ctx));
    ctx->format_context = avformat_alloc_context();
    if (!ctx->format_context)
        return AVERROR(ENOMEM);

    ctx->format_context->iformat = input_format;
    stream = avformat_new_stream(ctx->format_context, NULL);
    if (!stream) {
        avformat_free_context(ctx->format_context);
        ctx->format_context = NULL;
        return AVERROR(ENOMEM);
    }

    stream->codecpar->codec_type = AVMEDIA_TYPE_VIDEO;
    stream->pts_wrap_bits = pts_wrap_bits;
    stream->time_base = time_base;
    return 0;
}

static void
free_unwrap_context(
    coderctx_t *ctx)
{
    avformat_free_context(ctx->format_context);
    ctx->format_context = NULL;
}

static int
write_available_packets(
    AVFormatContext *format_context,
    AVCodecContext *codec_context,
    AVStream *stream,
    AVPacket *packet)
{
    int rc;

    while ((rc = avcodec_receive_packet(codec_context, packet)) >= 0) {
        av_packet_rescale_ts(packet, codec_context->time_base, stream->time_base);
        packet->stream_index = stream->index;
        rc = av_interleaved_write_frame(format_context, packet);
        av_packet_unref(packet);
        if (rc < 0)
            return rc;
    }

    if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF)
        return 0;
    return rc;
}

static void
fill_test_frame(
    AVFrame *frame,
    int frame_number)
{
    for (int y = 0; y < frame->height; y++)
        memset(frame->data[0] + y * frame->linesize[0],
            16 + (frame_number * 7) % 200, frame->width);

    for (int y = 0; y < frame->height / 2; y++) {
        memset(frame->data[1] + y * frame->linesize[1],
            96 + (frame_number * 3) % 64, frame->width / 2);
        memset(frame->data[2] + y * frame->linesize[2],
            160 - (frame_number * 3) % 64, frame->width / 2);
    }
}

/*
 * Generated tests encode MPEG-2 video with B-frames and mux as MPEG TS
 * around 33-bit timestamps.
 */
static int
generate_bframe_mpegts(
    const char *path,
    int64_t first_frame_pts)
{
    AVFormatContext *format_context = NULL;
    AVCodecContext *codec_context = NULL;
    const AVCodec *codec = NULL;
    AVStream *stream = NULL;
    AVFrame *frame = NULL;
    AVPacket *packet = NULL;
    int header_written = 0;
    int rc;

    rc = avformat_alloc_output_context2(&format_context, NULL, "mpegts", path);
    if (rc < 0)
        goto done;
    if (!format_context) {
        rc = AVERROR_UNKNOWN;
        goto done;
    }

    codec = avcodec_find_encoder(AV_CODEC_ID_MPEG2VIDEO);
    if (!codec) {
        rc = AVERROR_ENCODER_NOT_FOUND;
        goto done;
    }

    stream = avformat_new_stream(format_context, NULL);
    if (!stream) {
        rc = AVERROR(ENOMEM);
        goto done;
    }

    codec_context = avcodec_alloc_context3(codec);
    if (!codec_context) {
        rc = AVERROR(ENOMEM);
        goto done;
    }

    codec_context->codec_id = AV_CODEC_ID_MPEG2VIDEO;
    codec_context->codec_type = AVMEDIA_TYPE_VIDEO;
    codec_context->width = 64;
    codec_context->height = 64;
    codec_context->pix_fmt = AV_PIX_FMT_YUV420P;
    codec_context->time_base = VIDEO_TIME_BASE;
    codec_context->framerate = (AVRational){25, 1};
    codec_context->bit_rate = 400000;
    codec_context->gop_size = 12;
    codec_context->max_b_frames = 2;

    if (format_context->oformat->flags & AVFMT_GLOBALHEADER)
        codec_context->flags |= AV_CODEC_FLAG_GLOBAL_HEADER;

    rc = avcodec_open2(codec_context, codec, NULL);
    if (rc < 0)
        goto done;

    rc = avcodec_parameters_from_context(stream->codecpar, codec_context);
    if (rc < 0)
        goto done;
    stream->time_base = MPEGTS_TIME_BASE;

    /*
     * Preserve the large input timestamps so the muxer writes the real
     * 33-bit wrap instead of starting output stream at zero.
     */
    rc = av_opt_set_int(format_context->priv_data, "mpegts_copyts", 1, 0);
    if (rc < 0)
        goto done;
    format_context->max_delay = 0;
    format_context->avoid_negative_ts = AVFMT_AVOID_NEG_TS_DISABLED;

    rc = avio_open(&format_context->pb, path, AVIO_FLAG_WRITE);
    if (rc < 0)
        goto done;

    rc = avformat_write_header(format_context, NULL);
    if (rc < 0)
        goto done;
    header_written = 1;

    frame = av_frame_alloc();
    packet = av_packet_alloc();
    if (!frame || !packet) {
        rc = AVERROR(ENOMEM);
        goto done;
    }

    frame->format = codec_context->pix_fmt;
    frame->width = codec_context->width;
    frame->height = codec_context->height;
    rc = av_frame_get_buffer(frame, 32);
    if (rc < 0)
        goto done;

    for (int i = 0; i < GENERATED_FRAMES; i++) {
        rc = av_frame_make_writable(frame);
        if (rc < 0)
            goto done;

        fill_test_frame(frame, i);
        frame->pts = first_frame_pts + i;

        rc = avcodec_send_frame(codec_context, frame);
        if (rc < 0)
            goto done;
        rc = write_available_packets(format_context, codec_context, stream, packet);
        if (rc < 0)
            goto done;
    }

    rc = avcodec_send_frame(codec_context, NULL);
    if (rc < 0)
        goto done;
    rc = write_available_packets(format_context, codec_context, stream, packet);
    if (rc < 0)
        goto done;

    rc = av_write_trailer(format_context);
    header_written = 0;

done:
    if (header_written)
        av_write_trailer(format_context);
    av_packet_free(&packet);
    av_frame_free(&frame);
    avcodec_free_context(&codec_context);
    if (format_context && format_context->pb)
        avio_closep(&format_context->pb);
    avformat_free_context(format_context);
    return rc;
}

static int
compare_int64(
    const void *a,
    const void *b)
{
    const int64_t aa = *(const int64_t *)a;
    const int64_t bb = *(const int64_t *)b;
    return (aa > bb) - (aa < bb);
}

static unwrap_result_t
analyze_generated_mpegts(
    const char *path)
{
    unwrap_result_t result;
    AVFormatContext *format_context = NULL;
    AVDictionary *options = NULL;
    AVPacket *packet = NULL;
    coderctx_t *unwrap_context = NULL;
    pts_unwrapper_t *pts_unwrapper = NULL;
    pts_unwrapper_t *dts_unwrapper = NULL;
    int64_t presentation_pts[MAX_CAPTURED_PTS];
    int presentation_count = 0;
    int video_stream_index;
    int have_previous = 0;
    int64_t previous_pts = 0;
    int64_t previous_dts = 0;
    int rc;

    memset(&result, 0, sizeof(result));

    av_dict_set(&options, "correct_ts_overflow", "0", 0);
    rc = avformat_open_input(&format_context, path, NULL, &options);
    av_dict_free(&options);
    if (rc < 0)
        goto done;

    rc = avformat_find_stream_info(format_context, NULL);
    if (rc < 0)
        goto done;

    video_stream_index = av_find_best_stream(
        format_context, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0);
    if (video_stream_index < 0) {
        rc = video_stream_index;
        goto done;
    }

    unwrap_context = calloc(1, sizeof(*unwrap_context));
    if (!unwrap_context) {
        rc = AVERROR(ENOMEM);
        goto done;
    }
    unwrap_context->format_context = format_context;
    rc = pts_unwrap_init(unwrap_context);
    if (rc < 0)
        goto done;
    pts_unwrapper = &unwrap_context->pts_unwrapper[video_stream_index];
    dts_unwrapper = &unwrap_context->dts_unwrapper[video_stream_index];

    packet = av_packet_alloc();
    if (!packet) {
        rc = AVERROR(ENOMEM);
        goto done;
    }

    while ((rc = av_read_frame(format_context, packet)) >= 0) {
        if (packet->stream_index == video_stream_index) {
            int64_t old_pts_offset = pts_unwrapper->offset;
            int64_t old_dts_offset = dts_unwrapper->offset;
            int64_t pts;
            int64_t dts;

            result.packet_count++;
            if (packet->pts == AV_NOPTS_VALUE || packet->dts == AV_NOPTS_VALUE) {
                result.missing_timestamps++;
                av_packet_unref(packet);
                continue;
            }

            pts = pts_unwrap(pts_unwrapper, packet->pts);
            dts = pts_unwrap(dts_unwrapper, packet->dts);

            if (pts_unwrapper->offset > old_pts_offset)
                result.pts_forward_wraps++;
            else if (pts_unwrapper->offset < old_pts_offset)
                result.pts_backward_wraps++;

            if (dts_unwrapper->offset > old_dts_offset)
                result.dts_forward_wraps++;
            else if (dts_unwrapper->offset < old_dts_offset)
                result.dts_backward_wraps++;

            if (pts < dts)
                result.invalid_pts_dts++;

            if (have_previous) {
                if (pts < previous_pts)
                    result.pts_reorders++;
                if (dts <= previous_dts)
                    result.nonmonotonic_dts++;
                if (dts - previous_dts != FRAME_DURATION_TS)
                    result.dts_duration_errors++;
            }

            if (presentation_count < MAX_CAPTURED_PTS)
                presentation_pts[presentation_count++] = pts;

            previous_pts = pts;
            previous_dts = dts;
            have_previous = 1;
        }
        av_packet_unref(packet);
    }

    if (rc == AVERROR_EOF)
        rc = 0;
    if (rc < 0)
        goto done;

    qsort(presentation_pts, presentation_count,
        sizeof(presentation_pts[0]), compare_int64);
    for (int i = 1; i < presentation_count; i++) {
        if (presentation_pts[i] - presentation_pts[i - 1] != FRAME_DURATION_TS)
            result.presentation_gaps++;
    }

done:
    result.rc = rc;
    av_packet_free(&packet);
    free(unwrap_context);
    avformat_close_input(&format_context);
    return result;
}

static void
assert_generated_wrap_case(
    int first_frame_delta)
{
    char path[] = "/tmp/avpipe_pts_unwrap_XXXXXX";
    const int64_t wrap_frame = MPEGTS_WRAP_MODULUS / FRAME_DURATION_TS;
    unwrap_result_t result;
    int fd;
    int rc;

    fd = mkstemp(path);
    TEST_ASSERT_GREATER_OR_EQUAL_INT(0, fd);
    close(fd);

    rc = generate_bframe_mpegts(path, wrap_frame + first_frame_delta);
    if (rc != 0)
        unlink(path);
    TEST_ASSERT_EQUAL_INT_MESSAGE(0, rc, "failed to generate B-frame MPEG-TS");

    result = analyze_generated_mpegts(path);
    unlink(path);

    TEST_ASSERT_EQUAL_INT_MESSAGE(0, result.rc, "failed to demux generated MPEG-TS");
    TEST_ASSERT_EQUAL_INT(GENERATED_FRAMES, result.packet_count);
    TEST_ASSERT_EQUAL_INT(0, result.missing_timestamps);
    TEST_ASSERT_GREATER_THAN_INT_MESSAGE(0, result.pts_reorders,
        "generated stream did not contain decode-order PTS reordering");
    TEST_ASSERT_GREATER_THAN_INT_MESSAGE(0, result.pts_forward_wraps,
        "generated PTS did not cross the 33-bit wrap");
    TEST_ASSERT_GREATER_THAN_INT_MESSAGE(0, result.dts_forward_wraps,
        "generated DTS did not cross the 33-bit wrap");
    TEST_ASSERT_EQUAL_INT(0, result.dts_backward_wraps);
    TEST_ASSERT_EQUAL_INT(0, result.nonmonotonic_dts);
    TEST_ASSERT_EQUAL_INT(0, result.dts_duration_errors);
    TEST_ASSERT_EQUAL_INT(0, result.invalid_pts_dts);
    TEST_ASSERT_EQUAL_INT(0, result.presentation_gaps);
}

static void
assert_sequence(
    const int64_t *raw,
    const int64_t *expected,
    int count,
    int expected_forward_wraps,
    int expected_backward_wraps)
{
    pts_unwrapper_t unwrapper;
    int forward_wraps = 0;
    int backward_wraps = 0;

    memset(&unwrapper, 0, sizeof(unwrapper));
    unwrapper.wrap_modulus = MPEGTS_WRAP_MODULUS;

    for (int i = 0; i < count; i++) {
        int64_t old_offset = unwrapper.offset;
        int64_t actual = pts_unwrap(&unwrapper, raw[i]);
        TEST_ASSERT_EQUAL_INT64(expected[i], actual);
        if (unwrapper.offset > old_offset)
            forward_wraps++;
        else if (unwrapper.offset < old_offset)
            backward_wraps++;
    }

    TEST_ASSERT_EQUAL_INT(expected_forward_wraps, forward_wraps);
    TEST_ASSERT_EQUAL_INT(expected_backward_wraps, backward_wraps);
}

void
test_reordered_pts_crosses_wrap_in_both_directions(void)
{
    const int64_t m = MPEGTS_WRAP_MODULUS;
    const int64_t raw[] = {m - 4, 3, m - 1, 0};
    const int64_t expected[] = {m - 4, m + 3, m - 1, m};

    /*
     * A post-wrap reference frame is decoded before pre-wrap B-frames:
     * forward wrap, backward wrap for the B-frame, then forward again.
     */
    assert_sequence(raw, expected, 4, 2, 1);
}

void
test_non_mpegts_does_not_enable_custom_unwrap(void)
{
    coderctx_t ctx;

    TEST_ASSERT_EQUAL_INT(0, make_unwrap_context(
        &ctx, &test_non_mpegts_input_format, 32, (AVRational){1, 1000}));
    TEST_ASSERT_EQUAL_INT(0, pts_unwrap_init(&ctx));
    TEST_ASSERT_EQUAL_INT64(0, ctx.pts_unwrapper[0].wrap_modulus);
    TEST_ASSERT_EQUAL_INT64(0, ctx.dts_unwrapper[0].wrap_modulus);
    free_unwrap_context(&ctx);
}

void
test_mpegts_rejects_unexpected_wrap_bits(void)
{
    coderctx_t ctx;

    TEST_ASSERT_EQUAL_INT(0, make_unwrap_context(
        &ctx, &test_mpegts_input_format, 32, MPEGTS_TIME_BASE));
    TEST_ASSERT_EQUAL_INT(-1, pts_unwrap_init(&ctx));
    free_unwrap_context(&ctx);
}

void
test_mpegts_rejects_unexpected_time_base(void)
{
    coderctx_t ctx;

    TEST_ASSERT_EQUAL_INT(0, make_unwrap_context(
        &ctx, &test_mpegts_input_format, 33, (AVRational){1, 1000}));
    TEST_ASSERT_EQUAL_INT(-1, pts_unwrap_init(&ctx));
    free_unwrap_context(&ctx);
}

void
test_reordered_pts_crosses_a_later_wrap(void)
{
    const int64_t m = MPEGTS_WRAP_MODULUS;
    const int64_t raw[] = {m - 4, 3, m - 1, 0};
    const int64_t expected[] = {2 * m - 4, 2 * m + 3, 2 * m - 1, 2 * m};
    pts_unwrapper_t unwrapper;

    memset(&unwrapper, 0, sizeof(unwrapper));
    unwrapper.wrap_modulus = m;
    unwrapper.has_last = 1;
    unwrapper.last = m - 8;
    unwrapper.offset = m;

    for (int i = 0; i < 4; i++)
        TEST_ASSERT_EQUAL_INT64(expected[i], pts_unwrap(&unwrapper, raw[i]));
}

void
test_generated_ts_wrap_after_first_display_frame(void)
{
    assert_generated_wrap_case(0);
}

void
test_generated_ts_wrap_after_second_display_frame(void)
{
    assert_generated_wrap_case(-1);
}

void
test_generated_ts_wrap_later_in_bframe_gop(void)
{
    assert_generated_wrap_case(-3);
}

int
main(void)
{
    av_log_set_level(AV_LOG_ERROR);
    elv_logger_open("out", "test_pts_unwrap", 1, 1024 * 1024, elv_log_file);

    UNITY_BEGIN();

    RUN_TEST(test_non_mpegts_does_not_enable_custom_unwrap);
    RUN_TEST(test_mpegts_rejects_unexpected_wrap_bits);
    RUN_TEST(test_mpegts_rejects_unexpected_time_base);
    RUN_TEST(test_reordered_pts_crosses_wrap_in_both_directions);
    RUN_TEST(test_reordered_pts_crosses_a_later_wrap);
    RUN_TEST(test_generated_ts_wrap_after_first_display_frame);
    RUN_TEST(test_generated_ts_wrap_after_second_display_frame);
    RUN_TEST(test_generated_ts_wrap_later_in_bframe_gop);

    return UNITY_END();
}
