/*
 * Tests for the video fps filter (currently only RTMP)
 */

#include "unity/unity.h"

#include <string.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>
#include <libavutil/frame.h>

#include "../src/avpipe_filters.c"

#define TEST_WIDTH  64
#define TEST_HEIGHT 64
#define MAX_OUTPUT_FRAMES 32

typedef struct fps_test_context_t {
    coderctx_t decoder;
    coderctx_t encoder;
    xcparams_t params;
} fps_test_context_t;

typedef struct fps_test_output_t {
    int count;
    int64_t pts[MAX_OUTPUT_FRAMES];
    uint8_t luma[MAX_OUTPUT_FRAMES];
    AVRational time_base;
} fps_test_output_t;

void setUp(void)    {}
void tearDown(void) {}

static int
init_fps_test_context(
    fps_test_context_t *test,
    avp_live_proto_t live_proto,
    int video_fps)
{
    memset(test, 0, sizeof(*test));

    test->decoder.video_stream_index = 0;
    test->decoder.live_proto = live_proto;
    test->decoder.video_colorspace = AVCOL_SPC_BT709;
    test->decoder.video_color_range = AVCOL_RANGE_MPEG;
    test->decoder.format_context = avformat_alloc_context();
    test->decoder.codec_context[0] = avcodec_alloc_context3(NULL);
    test->encoder.codec_context[0] = avcodec_alloc_context3(NULL);
    if (!test->decoder.format_context || !test->decoder.codec_context[0] ||
        !test->encoder.codec_context[0])
        return AVERROR(ENOMEM);

    test->decoder.stream[0] = avformat_new_stream(test->decoder.format_context, NULL);
    if (!test->decoder.stream[0])
        return AVERROR(ENOMEM);
    test->decoder.stream[0]->avg_frame_rate = (AVRational) {30, 1};
    test->decoder.stream[0]->r_frame_rate = (AVRational) {30, 1};

    test->decoder.codec_context[0]->width = TEST_WIDTH;
    test->decoder.codec_context[0]->height = TEST_HEIGHT;
    test->decoder.codec_context[0]->pix_fmt = AV_PIX_FMT_YUV420P;
    test->decoder.codec_context[0]->sample_aspect_ratio = (AVRational) {1, 1};
    test->decoder.codec_context[0]->framerate = (AVRational) {30, 1};

    test->encoder.codec_context[0]->pix_fmt = AV_PIX_FMT_YUV420P;
    test->encoder.codec_context[0]->time_base = (AVRational) {1, 1000};

    test->params.url = (char *) "rtmp://unit.test/live";
    test->params.video_fps = video_fps;

    return init_video_filters("null", &test->decoder, &test->encoder, &test->params);
}

static void
free_fps_test_context(
    fps_test_context_t *test)
{
    avfilter_graph_free(&test->decoder.video_filter_graph);
    avcodec_free_context(&test->decoder.codec_context[0]);
    avcodec_free_context(&test->encoder.codec_context[0]);
    avformat_free_context(test->decoder.format_context);
}

static int
drain_available_frames(
    fps_test_context_t *test,
    fps_test_output_t *output)
{
    AVFrame *frame = av_frame_alloc();
    int rc;

    if (!frame)
        return AVERROR(ENOMEM);

    while ((rc = av_buffersink_get_frame(test->decoder.video_buffersink_ctx, frame)) >= 0) {
        if (output->count >= MAX_OUTPUT_FRAMES) {
            av_frame_free(&frame);
            return AVERROR(ENOSPC);
        }
        output->pts[output->count] = frame->pts;
        output->luma[output->count] = frame->data[0][0];
        output->count++;
        av_frame_unref(frame);
    }

    av_frame_free(&frame);
    if (rc == AVERROR(EAGAIN) || rc == AVERROR_EOF)
        return 0;
    return rc;
}

static int
push_test_frame(
    fps_test_context_t *test,
    fps_test_output_t *output,
    int64_t pts,
    int64_t duration,
    uint8_t luma)
{
    AVFrame *frame = av_frame_alloc();
    int rc;

    if (!frame)
        return AVERROR(ENOMEM);

    frame->format = AV_PIX_FMT_YUV420P;
    frame->width = TEST_WIDTH;
    frame->height = TEST_HEIGHT;
    frame->pts = pts;
    frame->duration = duration;
    frame->sample_aspect_ratio = (AVRational) {1, 1};
    frame->colorspace = AVCOL_SPC_BT709;
    frame->color_range = AVCOL_RANGE_MPEG;
    rc = av_frame_get_buffer(frame, 0);
    if (rc < 0)
        goto done;

    memset(frame->data[0], luma, frame->linesize[0] * frame->height);
    memset(frame->data[1], 128, frame->linesize[1] * frame->height / 2);
    memset(frame->data[2], 128, frame->linesize[2] * frame->height / 2);

    rc = av_buffersrc_add_frame_flags(test->decoder.video_buffersrc_ctx, frame,
        AV_BUFFERSRC_FLAG_KEEP_REF);
    if (rc >= 0)
        rc = drain_available_frames(test, output);

done:
    av_frame_free(&frame);
    return rc;
}

static int
finish_test_stream(
    fps_test_context_t *test,
    fps_test_output_t *output)
{
    int rc = av_buffersrc_add_frame_flags(test->decoder.video_buffersrc_ctx, NULL, 0);
    if (rc < 0 && rc != AVERROR_EOF)
        return rc;
    output->time_base = av_buffersink_get_time_base(test->decoder.video_buffersink_ctx);
    return drain_available_frames(test, output);
}

static int
run_test_stream(
    fps_test_context_t *test,
    fps_test_output_t *output)
{
    static const int64_t pts[] = {0, 33, 100, 133};
    int rc = 0;

    memset(output, 0, sizeof(*output));
    for (int i = 0; i < 4 && rc >= 0; i++)
        rc = push_test_frame(test, output, pts[i], 33, (uint8_t) (20 + i));
    if (rc >= 0)
        rc = finish_test_stream(test, output);
    return rc;
}

void
test_default_does_not_change_frame_pacing(void)
{
    fps_test_context_t test;
    fps_test_output_t output;
    static const int64_t expected_pts[] = {0, 33, 100, 133};

    TEST_ASSERT_EQUAL_INT(0, init_fps_test_context(&test, avp_proto_rtmp, 0));
    TEST_ASSERT_EQUAL_INT(0, run_test_stream(&test, &output));
    TEST_ASSERT_EQUAL_INT(4, output.count);
    TEST_ASSERT_EQUAL_INT(1, output.time_base.num);
    TEST_ASSERT_EQUAL_INT(1000, output.time_base.den);
    for (int i = 0; i < output.count; i++)
        TEST_ASSERT_EQUAL_INT64(expected_pts[i], output.pts[i]);
    free_fps_test_context(&test);
}

void
test_rtmp_fps_fills_timestamp_holes(void)
{
    fps_test_context_t test;
    fps_test_output_t output;
    static const int64_t expected_pts[] = {0, 33, 67, 100, 133};
    int duplicate_count = 0;

    TEST_ASSERT_EQUAL_INT(0, init_fps_test_context(&test, avp_proto_rtmp, 30));
    TEST_ASSERT_EQUAL_INT(0, run_test_stream(&test, &output));
    TEST_ASSERT_EQUAL_INT(5, output.count);
    TEST_ASSERT_EQUAL_INT(1, output.time_base.num);
    TEST_ASSERT_EQUAL_INT(1000, output.time_base.den);
    for (int i = 0; i < output.count; i++) {
        TEST_ASSERT_EQUAL_INT64(expected_pts[i], output.pts[i]);
        if (i > 0 && output.luma[i] == output.luma[i - 1])
            duplicate_count++;
    }
    TEST_ASSERT_GREATER_THAN_INT(0, duplicate_count);
    free_fps_test_context(&test);
}

void
test_video_fps_must_match_source_nominal_frame_rate(void)
{
    fps_test_context_t test;

    TEST_ASSERT_EQUAL_INT(0, init_fps_test_context(&test, avp_proto_rtmp, 0));
    test.params.video_fps = 25;
    TEST_ASSERT_EQUAL_INT(eav_param, validate_video_fps(&test.decoder, &test.params));
    test.params.video_fps = 30;
    TEST_ASSERT_EQUAL_INT(eav_success, validate_video_fps(&test.decoder, &test.params));
    free_fps_test_context(&test);
}

void
test_video_fps_is_not_inserted_for_non_rtmp_graph(void)
{
    fps_test_context_t test;
    fps_test_output_t output;

    TEST_ASSERT_EQUAL_INT(0, init_fps_test_context(&test, avp_proto_srt, 30));
    TEST_ASSERT_EQUAL_INT(0, run_test_stream(&test, &output));
    TEST_ASSERT_EQUAL_INT(4, output.count);
    TEST_ASSERT_EQUAL_INT(1, output.time_base.num);
    TEST_ASSERT_EQUAL_INT(1000, output.time_base.den);
    free_fps_test_context(&test);
}

int
main(void)
{
    UNITY_BEGIN();
    RUN_TEST(test_default_does_not_change_frame_pacing);
    RUN_TEST(test_rtmp_fps_fills_timestamp_holes);
    RUN_TEST(test_video_fps_is_not_inserted_for_non_rtmp_graph);
    RUN_TEST(test_video_fps_must_match_source_nominal_frame_rate);
    return UNITY_END();
}
