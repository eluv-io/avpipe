/*
 * test_fade_filter.c
 *
 * Unit tests for append_fade_filter() in avpipe_filters.c - the generated fade
 * filter string:
 *   - the blend-based fade must hold its end level past the fade window rather
 *     than snapping back to full brightness, and must emit enough coefficient
 *     precision for a long fade to actually converge on that level
 *   - the pre-canned "in"/"out" path (no levels set) is unaffected
 *
 * We include avpipe_filters.c directly so the unit under test and its static
 * helpers are accessible, matching the other tests in this directory.
 */

#include "unity/unity.h"

#include <string.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/rational.h>

/* Pull in the unit under test. */
#include "../src/avpipe_filters.c"

/*
 * avpipe_filters.c references vertical_data_crop_x() (defined in avpipe_utils.c)
 * from crop_send_command(). These tests exercise neither, so stub it rather than
 * drag the whole avpipe_utils.c dependency chain into the test binary.
 */
int
vertical_data_crop_x(uint8_t *vertical_data, int data_len, int frame_idx,
    int scaled_width, int crop_width)
{
    (void)vertical_data; (void)data_len; (void)frame_idx;
    (void)scaled_width; (void)crop_width;
    return 0;
}

void setUp(void)    {}
void tearDown(void) {}

/*
 * Fixture: an encoder context with one video stream. All of these tests use
 * start_time_ts == 0, so filter_frame_offset() returns 0 without reading the
 * timebase/frame-rate fields - but they are populated anyway so the fixture does
 * not depend on that early return.
 *
 * FILTER_STRING_SZ is #define'd in avpipe_xc.c rather than a header, so the
 * filter buffers here use a literal size.
 */
typedef struct fade_fixture_t {
    coderctx_t      encoder;
    AVStream        enc_stream;
    AVCodecContext  enc_codec;
    xcparams_t      params;
} fade_fixture_t;

static void
fixture_init(
    fade_fixture_t *f)
{
    memset(f, 0, sizeof(*f));

    f->enc_stream.avg_frame_rate = (AVRational){ 30, 1 };
    f->enc_codec.time_base       = (AVRational){ 1, 30000 };

    f->encoder.video_stream_index = 0;
    f->encoder.stream[0]          = &f->enc_stream;
    f->encoder.codec_context[0]   = &f->enc_codec;

    f->params.start_time_ts = 0;
    f->params.skip_decoding = 0;
    f->params.fade          = NULL;
}

/* Build the blend-based fade string for the given window and levels. */
static void
build_blend_fade(
    char *buf,
    size_t buf_sz,
    int start_frame,
    int end_frame,
    double level_1,
    double level_2)
{
    fade_fixture_t f;
    fixture_init(&f);
    f.params.fade             = "out";
    f.params.fade_start_frame = start_frame;
    f.params.fade_end_frame   = end_frame;
    f.params.fade_level_1     = level_1;
    f.params.fade_level_2     = level_2;

    buf[0] = '\0';
    TEST_ASSERT_EQUAL_INT(0,
        append_fade_filter(buf, buf_sz, &f.encoder, &f.params));
}

/* The blend must stay enabled for every frame at or after the start frame and
 * clamp N to the end frame, so brightness holds level2 past the fade instead of
 * snapping back to full when 'enable' turns off. */
void
test_blend_fade_holds_after_end_frame(void)
{
    char buf[4096];
    build_blend_fade(buf, sizeof(buf), 0, 120, 1.0, 0.0);

    TEST_ASSERT_NOT_NULL(strstr(buf, "min(N,120)"));
    TEST_ASSERT_NOT_NULL(strstr(buf, "enable='gte(n,0)'"));
    TEST_ASSERT_NULL(strstr(buf, "between(n,"));
}

/* Coefficients must be emitted with enough precision that a long fade actually
 * converges: 1.0 -> 0.0 over 120 frames is rate -1/120 = -0.008333..., which
 * truncated to 3 decimals (-0.008) leaves a ~4% residual at the end frame. */
void
test_blend_fade_rate_precision(void)
{
    char buf[4096];
    build_blend_fade(buf, sizeof(buf), 0, 120, 1.0, 0.0);

    TEST_ASSERT_NOT_NULL(strstr(buf, "1.000000"));
    TEST_ASSERT_NOT_NULL(strstr(buf, "-0.008333"));
    TEST_ASSERT_NULL(strstr(buf, "-0.008*"));
}

/* The pre-canned "in"/"out" fade path (no levels) is unchanged. */
void
test_simple_fade_out_uses_fade_filter(void)
{
    char buf[4096];
    fade_fixture_t f;
    fixture_init(&f);
    f.params.fade = "out";

    buf[0] = '\0';
    TEST_ASSERT_EQUAL_INT(0,
        append_fade_filter(buf, sizeof(buf), &f.encoder, &f.params));

    TEST_ASSERT_NOT_NULL(strstr(buf, "fade=t=out"));
    TEST_ASSERT_NULL(strstr(buf, "blend="));
}

int
main(void)
{
    av_log_set_level(AV_LOG_ERROR);
    elv_logger_open("out", "test_fade_filter", 1, 1024 * 1024, elv_log_file);

    UNITY_BEGIN();

    RUN_TEST(test_blend_fade_holds_after_end_frame);
    RUN_TEST(test_blend_fade_rate_precision);
    RUN_TEST(test_simple_fade_out_uses_fade_filter);

    return UNITY_END();
}
