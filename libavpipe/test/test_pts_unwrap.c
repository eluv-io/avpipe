/*
 * Unit tests for pts_unwrap() in avpipe_format.c.
 */

#include "unity/unity.h"

#include <string.h>
#include <libavcodec/avcodec.h>

#include "../src/avpipe_format.c"

#define PTS_WRAP_MODULUS (INT64_C(1) << 33)

void setUp(void) {}
void tearDown(void) {}

static pts_unwrapper_t
make_unwrapper(void)
{
    pts_unwrapper_t unwrapper;

    memset(&unwrapper, 0, sizeof(unwrapper));
    unwrapper.wrap_modulus = PTS_WRAP_MODULUS;
    return unwrapper;
}

void test_no_wrap_preserves_timestamps(void)
{
    pts_unwrapper_t unwrapper = make_unwrapper();

    TEST_ASSERT_EQUAL_INT64(100, pts_unwrap(&unwrapper, 100));
    TEST_ASSERT_EQUAL_INT64(200, pts_unwrap(&unwrapper, 200));
    TEST_ASSERT_EQUAL_INT64(300, pts_unwrap(&unwrapper, 300));
}

void test_forward_wrap_advances_epoch(void)
{
    pts_unwrapper_t unwrapper = make_unwrapper();

    TEST_ASSERT_EQUAL_INT64(PTS_WRAP_MODULUS - 10,
        pts_unwrap(&unwrapper, PTS_WRAP_MODULUS - 10));
    TEST_ASSERT_EQUAL_INT64(PTS_WRAP_MODULUS + 20, pts_unwrap(&unwrapper, 20));
}

void test_reordered_packet_returns_to_previous_epoch(void)
{
    pts_unwrapper_t unwrapper = make_unwrapper();

    pts_unwrap(&unwrapper, PTS_WRAP_MODULUS - 10);
    pts_unwrap(&unwrapper, 20);
    TEST_ASSERT_EQUAL_INT64(PTS_WRAP_MODULUS - 5,
        pts_unwrap(&unwrapper, PTS_WRAP_MODULUS - 5));
}

void test_no_pts_is_unchanged(void)
{
    pts_unwrapper_t unwrapper = make_unwrapper();

    TEST_ASSERT_EQUAL_INT64(AV_NOPTS_VALUE, pts_unwrap(&unwrapper, AV_NOPTS_VALUE));
    TEST_ASSERT_FALSE(unwrapper.has_last);
}

void test_init_resets_existing_unwrap_state(void)
{
    coderctx_t ctx;
    AVFormatContext *format_context = avformat_alloc_context();
    AVStream *stream;

    TEST_ASSERT_NOT_NULL(format_context);
    stream = avformat_new_stream(format_context, NULL);
    TEST_ASSERT_NOT_NULL(stream);
    stream->pts_wrap_bits = 33;

    memset(&ctx, 0, sizeof(ctx));
    ctx.format_context = format_context;
    ctx.pts_unwrapper[0].has_last = 1;
    ctx.pts_unwrapper[0].last = 123;
    ctx.pts_unwrapper[0].offset = PTS_WRAP_MODULUS;
    ctx.dts_unwrapper[0].has_last = 1;
    ctx.dts_unwrapper[0].last = 456;
    ctx.dts_unwrapper[0].offset = PTS_WRAP_MODULUS;

    pts_unwrap_init(&ctx);

    TEST_ASSERT_FALSE(ctx.pts_unwrapper[0].has_last);
    TEST_ASSERT_EQUAL_INT64(0, ctx.pts_unwrapper[0].last);
    TEST_ASSERT_EQUAL_INT64(0, ctx.pts_unwrapper[0].offset);
    TEST_ASSERT_FALSE(ctx.dts_unwrapper[0].has_last);
    TEST_ASSERT_EQUAL_INT64(0, ctx.dts_unwrapper[0].last);
    TEST_ASSERT_EQUAL_INT64(0, ctx.dts_unwrapper[0].offset);

    avformat_free_context(format_context);
}

int main(void)
{
    elv_logger_open("out", "test_pts_unwrap", 1, 1024 * 1024, elv_log_file);

    UNITY_BEGIN();
    RUN_TEST(test_no_wrap_preserves_timestamps);
    RUN_TEST(test_forward_wrap_advances_epoch);
    RUN_TEST(test_reordered_packet_returns_to_previous_epoch);
    RUN_TEST(test_no_pts_is_unchanged);
    RUN_TEST(test_init_resets_existing_unwrap_state);
    return UNITY_END();
}
