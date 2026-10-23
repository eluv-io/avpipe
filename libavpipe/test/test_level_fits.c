/*
 * test_level_fits.c
 *
 * Unit tests for the level tables in avpipe_level.c: avpipe_check_level,
 * avpipe_check_hevc_level, avpipe_h264_level_fits and avpipe_hevc_level_fits.
 *
 * The limits asserted here are the ones that decide whether an encoder is given
 * the caller's level or left to pick its own; nvenc refuses to open on a level
 * the picture does not fit, so these are the cases the Go tests cannot cover
 * without a GPU.
 */

#include "unity/unity.h"

/* Pull in the unit under test. */
#include "../src/avpipe_level.c"

void setUp(void) {}
void tearDown(void) {}

/* ---------------------------------------------------------------------------
 * Level sets
 * ---------------------------------------------------------------------------*/

static void
test_h264_level_set(void)
{
    TEST_ASSERT_EQUAL_INT(1, avpipe_check_level(0));
    TEST_ASSERT_EQUAL_INT(1, avpipe_check_level(9));   /* 1b */
    TEST_ASSERT_EQUAL_INT(1, avpipe_check_level(41));
    TEST_ASSERT_EQUAL_INT(-1, avpipe_check_level(93)); /* HEVC 3.1 */
    TEST_ASSERT_EQUAL_INT(-1, avpipe_check_level(45));
}

static void
test_hevc_level_set(void)
{
    TEST_ASSERT_EQUAL_INT(1, avpipe_check_hevc_level(0));
    TEST_ASSERT_EQUAL_INT(1, avpipe_check_hevc_level(123));
    TEST_ASSERT_EQUAL_INT(1, avpipe_check_hevc_level(30));  /* also H.264 3.0 */
    TEST_ASSERT_EQUAL_INT(-1, avpipe_check_hevc_level(41)); /* H.264 4.1 */
    TEST_ASSERT_EQUAL_INT(-1, avpipe_check_hevc_level(100));
}

/* ---------------------------------------------------------------------------
 * H.264 Table A-1
 * ---------------------------------------------------------------------------*/

/* 1280x720 is 3600 macroblocks: over 3.0's MaxFS (1620), under 3.1's (3600). */
static void
test_h264_720p30_fits_31_not_30(void)
{
    TEST_ASSERT_EQUAL_INT(0, avpipe_h264_level_fits(30, 1280, 720, 30, 0));
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(31, 1280, 720, 30, 0));
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(41, 1280, 720, 30, 0));
}

/* 1080p60 is 489600 macroblocks per second: over 4.0's MaxMBPS (245760), under 4.2's. */
static void
test_h264_1080p60_fits_42_not_40(void)
{
    TEST_ASSERT_EQUAL_INT(0, avpipe_h264_level_fits(40, 1920, 1080, 60, 0));
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(42, 1920, 1080, 60, 0));
    /* The same picture at 30 fps fits 4.0. */
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(40, 1920, 1080, 30, 0));
}

/* A 16-frame DPB of 720x480 (1350 macroblocks) is 21600 macroblocks: over 3.2's
 * MaxDpbMbs (20480), under 4.0's (32768). */
static void
test_h264_dpb_limit(void)
{
    TEST_ASSERT_EQUAL_INT(0, avpipe_h264_level_fits(32, 720, 480, 30, 16));
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(40, 720, 480, 30, 16));
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(31, 720, 480, 30, 3));
}

static void
test_h264_unknown_fps_and_dpb_skip_their_checks(void)
{
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(40, 1920, 1080, 0, 0));
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(32, 720, 480, 30, 0));
}

/* 11 is level 1.1, not the 1b alias that shares the level_idc. */
static void
test_h264_level_11_is_1_1(void)
{
    /* 1.1: MaxFS 396, MaxMBPS 3000. 352x288 is 396 macroblocks. */
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(11, 352, 288, 7, 0));
    TEST_ASSERT_EQUAL_INT(0, avpipe_h264_level_fits(11, 352, 288, 15, 0));
    /* 1b itself (level_idc 9): MaxMBPS 1485 holds 176x144 (99 macroblocks) at 15 fps. */
    TEST_ASSERT_EQUAL_INT(1, avpipe_h264_level_fits(9, 176, 144, 15, 0));
}

static void
test_h264_unknown_level(void)
{
    TEST_ASSERT_EQUAL_INT(-1, avpipe_h264_level_fits(45, 1280, 720, 30, 0));
    TEST_ASSERT_EQUAL_INT(-1, avpipe_h264_level_fits(93, 1280, 720, 30, 0));
}

/* ---------------------------------------------------------------------------
 * HEVC Table A.8
 * ---------------------------------------------------------------------------*/

/* 1280x720 is 921600 luma samples: over 3.0's MaxLumaPs (552960), under 3.1's. */
static void
test_hevc_720p30_fits_93_not_90(void)
{
    TEST_ASSERT_EQUAL_INT(0, avpipe_hevc_level_fits(90, 1280, 720, 30));
    TEST_ASSERT_EQUAL_INT(1, avpipe_hevc_level_fits(93, 1280, 720, 30));
}

/* 1080p60 is 124416000 luma samples per second: over 4.0's MaxLumaSr (66846720),
 * under 4.1's (133693440); the picture itself fits both. */
static void
test_hevc_1080p60_fits_123_not_120(void)
{
    TEST_ASSERT_EQUAL_INT(0, avpipe_hevc_level_fits(120, 1920, 1080, 60));
    TEST_ASSERT_EQUAL_INT(1, avpipe_hevc_level_fits(123, 1920, 1080, 60));
    TEST_ASSERT_EQUAL_INT(1, avpipe_hevc_level_fits(120, 1920, 1080, 30));
}

static void
test_hevc_unknown_fps_skips_rate_check(void)
{
    TEST_ASSERT_EQUAL_INT(1, avpipe_hevc_level_fits(120, 1920, 1080, 0));
}

static void
test_hevc_unknown_level(void)
{
    TEST_ASSERT_EQUAL_INT(-1, avpipe_hevc_level_fits(41, 1280, 720, 30));
    TEST_ASSERT_EQUAL_INT(-1, avpipe_hevc_level_fits(100, 1280, 720, 30));
}

int main(void)
{
    UNITY_BEGIN();

    RUN_TEST(test_h264_level_set);
    RUN_TEST(test_hevc_level_set);

    RUN_TEST(test_h264_720p30_fits_31_not_30);
    RUN_TEST(test_h264_1080p60_fits_42_not_40);
    RUN_TEST(test_h264_dpb_limit);
    RUN_TEST(test_h264_unknown_fps_and_dpb_skip_their_checks);
    RUN_TEST(test_h264_level_11_is_1_1);
    RUN_TEST(test_h264_unknown_level);

    RUN_TEST(test_hevc_720p30_fits_93_not_90);
    RUN_TEST(test_hevc_1080p60_fits_123_not_120);
    RUN_TEST(test_hevc_unknown_fps_skips_rate_check);
    RUN_TEST(test_hevc_unknown_level);

    return UNITY_END();
}
