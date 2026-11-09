#include "unity/unity.h"
#include <limits.h>
#include "../src/avpipe_format.c"

void setUp(void) {}
void tearDown(void) {}

/* Direct-rate cases still use the same production context-based entry point. */
static int check_rates(AVRational time_base, AVRational frame_rate)
{
    AVCodecContext encoder = {.time_base = time_base, .framerate = frame_rate};
    AVStream stream = {.avg_frame_rate = {0, 1}, .r_frame_rate = {0, 1}};
    coderctx_t decoder = {.video_stream_index = 0, .stream = {&stream}};
    xcparams_t params = {.url = "test", .deinterlace = dif_bwdif};
    return check_video_timebase(&encoder, &decoder, &params);
}

static void test_frame_rate_precedence_and_deinterlacing(void)
{
    AVCodecContext encoder = {.time_base = {1, 25}, .framerate = {25, 1}};
    AVStream stream = {.avg_frame_rate = {25, 1}, .r_frame_rate = {30, 1}};
    coderctx_t decoder = {.video_stream_index = 0, .stream = {&stream}};
    xcparams_t params = {.url = "test", .deinterlace = dif_bwdif};

    /* An existing encoder rate is final and must not be doubled again. */
    TEST_ASSERT_EQUAL(eav_success, check_video_timebase(&encoder, &decoder, &params));

    encoder.framerate = (AVRational){0, 1};
    /* Fallback source rate 25 becomes 50: 1/25 is now too coarse. */
    TEST_ASSERT_EQUAL(eav_timebase, check_video_timebase(&encoder, &decoder, &params));
    encoder.time_base = (AVRational){1, 50};
    /* avg_frame_rate takes precedence over r_frame_rate (30 -> 60 would fail). */
    TEST_ASSERT_EQUAL(eav_success, check_video_timebase(&encoder, &decoder, &params));

    stream.avg_frame_rate = (AVRational){0, 1};
    stream.r_frame_rate = (AVRational){25, 1};
    encoder.time_base = (AVRational){1, 25};
    TEST_ASSERT_EQUAL(eav_timebase, check_video_timebase(&encoder, &decoder, &params));
    params.deinterlace = dif_bwdif_frame;
    TEST_ASSERT_EQUAL(eav_success, check_video_timebase(&encoder, &decoder, &params));
    params.deinterlace = dif_none;
    TEST_ASSERT_EQUAL(eav_success, check_video_timebase(&encoder, &decoder, &params));

    TEST_ASSERT_EQUAL(0, encoder.framerate.num); /* Validation does not configure the encoder. */
}

static void test_integer_frame_ticks(void)
{
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 12800}, (AVRational){50, 1}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 90000}, (AVRational){50, 1}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 60000}, (AVRational){60000, 1001}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 90000}, (AVRational){30000, 1001}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1001, 60000}, (AVRational){60000, 1001}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){2, 100}, (AVRational){50, 1}));
}

static void test_adjacent_integer_frame_durations(void)
{
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 10000}, (AVRational){24, 1}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 1000}, (AVRational){24, 1}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 90000}, (AVRational){60000, 1001}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 23976}, (AVRational){24000, 1001}));
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, 1000}, (AVRational){999, 1}));
}

static void test_subtick_frame_durations_rejected(void)
{
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){1, 25}, (AVRational){50, 1}));
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){1, 1000}, (AVRational){1001, 1}));
}

static void test_invalid_rates_and_timebases(void)
{
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){0, 1}, (AVRational){50, 1}));
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){1, 0}, (AVRational){50, 1}));
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){-1, 50}, (AVRational){50, 1}));
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){1, 50}, (AVRational){0, 1}));
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){1, 50}, (AVRational){50, 0}));
}

static void test_large_rational_components(void)
{
    TEST_ASSERT_EQUAL(eav_success, check_rates((AVRational){1, INT_MAX}, (AVRational){INT_MAX, INT_MAX}));
    TEST_ASSERT_EQUAL(eav_timebase, check_rates((AVRational){INT_MAX, 1}, (AVRational){INT_MAX, 1}));
}

static void test_regular_transcodes_allow_fractional_frame_ticks(void)
{
    AVCodecContext encoder = {.time_base = {1, 10000}, .framerate = {24, 1}};
    coderctx_t decoder = {0};
    xcparams_t params = {.url = "test", .deinterlace = dif_none};
    TEST_ASSERT_EQUAL(eav_success, check_video_timebase(&encoder, &decoder, &params));

    /* RTMP/FLV uses millisecond timestamps (24 fps -> 41/42 ticks). */
    encoder.time_base = (AVRational){1, 1000};
    TEST_ASSERT_EQUAL(eav_success, check_video_timebase(&encoder, &decoder, &params));

    encoder.time_base = (AVRational){1, 90000};
    encoder.framerate = (AVRational){60000, 1001};
    TEST_ASSERT_EQUAL(eav_success, check_video_timebase(&encoder, &decoder, &params));

    encoder.time_base = (AVRational){1, 25};
    TEST_ASSERT_EQUAL(eav_timebase, check_video_timebase(&encoder, &decoder, &params));
}

int main(void)
{
    UNITY_BEGIN();
    RUN_TEST(test_frame_rate_precedence_and_deinterlacing);
    RUN_TEST(test_integer_frame_ticks);
    RUN_TEST(test_adjacent_integer_frame_durations);
    RUN_TEST(test_subtick_frame_durations_rejected);
    RUN_TEST(test_invalid_rates_and_timebases);
    RUN_TEST(test_large_rational_components);
    RUN_TEST(test_regular_transcodes_allow_fractional_frame_ticks);
    return UNITY_END();
}
