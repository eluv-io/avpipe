/*
 * Unit tests for the text a clock slot shows (ov_clock_text).
 */

#include "unity/unity.h"

#include <string.h>

#include <libavutil/pixdesc.h>

#include "../src/avpipe_overlay.c"

void setUp(void) {}
void tearDown(void) {}

/* The text of a clock holding ms, elapsed_ms after its event */
static const char *
clock_text(
    ov_clock_format format,
    int64_t ms,
    int running,
    int up,
    int64_t elapsed_ms)
{
    static char buf[32];
    ov_slot_t slot = {0};
    ov_clock_t clock = {.set = 1, .ms = ms, .running = running, .up = up};
    slot.clock_format = format;
    ov_clock_text(buf, sizeof(buf), &slot, &clock, elapsed_ms);
    return buf;
}

/* A countdown shows the second it is in */
static void
test_mmss_counts_the_second_it_is_in(void)
{
    TEST_ASSERT_EQUAL_STRING("00:03", clock_text(ov_clock_mmss, 2300, 0, 0, 0));
    TEST_ASSERT_EQUAL_STRING("12:00", clock_text(ov_clock_mmss, 720000, 0, 0, 0));
    TEST_ASSERT_EQUAL_STRING("11:59", clock_text(ov_clock_mmss, 720000, 1, 0, 1000));
    TEST_ASSERT_EQUAL_STRING("00:00", clock_text(ov_clock_mmss, 500, 1, 0, 2000));
    TEST_ASSERT_EQUAL_STRING("1:00:00", clock_text(ov_clock_hmmss, 3600000, 0, 0, 0));
}

static void
test_game_shows_tenths_under_a_minute(void)
{
    TEST_ASSERT_EQUAL_STRING("1:00", clock_text(ov_clock_game, 60000, 0, 0, 0));
    TEST_ASSERT_EQUAL_STRING("59.9", clock_text(ov_clock_game, 59900, 0, 0, 0));
    TEST_ASSERT_EQUAL_STRING("0.0", clock_text(ov_clock_game, 0, 0, 0, 0));
}

/* A shot clock is cut off: it reads 5 for the whole second from 5.999 down to 5.000, then 4.9 */
static void
test_shot_is_cut_off(void)
{
    TEST_ASSERT_EQUAL_STRING("24", clock_text(ov_clock_shot, 24000, 0, 0, 0));
    TEST_ASSERT_EQUAL_STRING("23", clock_text(ov_clock_shot, 23999, 1, 0, 0));
    TEST_ASSERT_EQUAL_STRING("23", clock_text(ov_clock_shot, 23999, 1, 0, 999));
    TEST_ASSERT_EQUAL_STRING("22", clock_text(ov_clock_shot, 23999, 1, 0, 1000));
    TEST_ASSERT_EQUAL_STRING("5", clock_text(ov_clock_shot, 5999, 1, 0, 0));
    TEST_ASSERT_EQUAL_STRING("5", clock_text(ov_clock_shot, 5999, 1, 0, 999));
    TEST_ASSERT_EQUAL_STRING("4.9", clock_text(ov_clock_shot, 5999, 1, 0, 1000));
    TEST_ASSERT_EQUAL_STRING("4.9", clock_text(ov_clock_shot, 4999, 1, 0, 99));
    TEST_ASSERT_EQUAL_STRING("4.8", clock_text(ov_clock_shot, 4999, 1, 0, 100));
    TEST_ASSERT_EQUAL_STRING("0.0", clock_text(ov_clock_shot, 99, 1, 0, 0));
    TEST_ASSERT_EQUAL_STRING("0.0", clock_text(ov_clock_shot, 99, 1, 0, 5000));
    TEST_ASSERT_EQUAL_STRING("13", clock_text(ov_clock_shot, 13000, 0, 0, 7000));
}

int
main(void)
{
    UNITY_BEGIN();
    RUN_TEST(test_mmss_counts_the_second_it_is_in);
    RUN_TEST(test_game_shows_tenths_under_a_minute);
    RUN_TEST(test_shot_is_cut_off);
    return UNITY_END();
}
