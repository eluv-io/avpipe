/*
 * test_io_open.c
 *
 * Unit tests for elv_io_open()/elv_io_close() in avpipe_io.c, driven through
 * stub output handlers so the opener can be made to fail on demand.
 *
 * The property under test: out_tracker->last_outctx only ever points at an
 * outctx that opened successfully. encode_frame and do_bypass write stats
 * through last_outctx, so leaving it pointing at an outctx that elv_io_open
 * freed after a failed open is a use-after-free.
 */

#include "unity/unity.h"

#include <string.h>
#include <libavformat/avformat.h>
#include <libavutil/dict.h>
#include <libavutil/mem.h>

/* Pull in the unit under test, including its static helpers. */
#include "../src/avpipe_io.c"

/* ---------------------------------------------------------------------------
 * Stub output handlers
 * ---------------------------------------------------------------------------*/

static int      opener_rc;      /* what the stub opener returns */
static ioctx_t  *opened_outctx; /* the outctx the opener was last called with */
static int      opener_calls;
static int      closer_calls;
static int      stater_calls;

static int
stub_opener(
    const char *url,
    ioctx_t *outctx)
{
    opener_calls++;
    opened_outctx = outctx;
    if (opener_rc < 0)
        return opener_rc;
    /* Like out_opener: the AVIOContext takes ownership of buf. */
    outctx->bufsz = 4096;
    outctx->buf = av_malloc(outctx->bufsz);
    return 0;
}

static int
stub_closer(
    ioctx_t *outctx)
{
    closer_calls++;
    return 0;
}

static int
stub_stater(
    void *opaque,
    int src_stream_index,
    avp_stat_t stat_type)
{
    stater_calls++;
    return 0;
}

static avpipe_io_handler_t handlers = {
    .avpipe_opener = stub_opener,
    .avpipe_closer = stub_closer,
    .avpipe_stater = stub_stater,
};

static out_tracker_t    tracker;
static AVFormatContext  format_ctx;
static ioctx_t          prev_outctx;    /* stands in for an earlier, still-open output */

void setUp(void)
{
    opener_rc = 0;
    opened_outctx = NULL;
    opener_calls = closer_calls = stater_calls = 0;

    memset(&tracker, 0, sizeof(tracker));
    tracker.out_handlers = &handlers;
    tracker.xc_type = xc_video;

    memset(&format_ctx, 0, sizeof(format_ctx));
    format_ctx.avpipe_opaque = &tracker;
}

void tearDown(void) {}

/* ---------------------------------------------------------------------------
 * Opener failure
 * ---------------------------------------------------------------------------*/

void test_failed_segment_open_keeps_last_outctx(void)
{
    AVIOContext *pb = NULL;

    tracker.last_outctx = &prev_outctx;
    opener_rc = -1;

    int rc = elv_io_open(&format_ctx, &pb, "fsegment-video-00002.mp4", AVIO_FLAG_WRITE, NULL);

    TEST_ASSERT_EQUAL_INT(-1, rc);
    TEST_ASSERT_EQUAL_INT(1, opener_calls);
    TEST_ASSERT_NULL(pb);
    /* The failed outctx is freed: last_outctx must not have been pointed at it. */
    TEST_ASSERT_TRUE(tracker.last_outctx != opened_outctx);
    TEST_ASSERT_EQUAL_PTR(&prev_outctx, tracker.last_outctx);
    TEST_ASSERT_EQUAL_INT(0, stater_calls);
}

void test_failed_segment_open_with_no_previous_output(void)
{
    AVIOContext *pb = NULL;

    opener_rc = -1;

    int rc = elv_io_open(&format_ctx, &pb, "fsegment-video-00001.mp4", AVIO_FLAG_WRITE, NULL);

    TEST_ASSERT_EQUAL_INT(-1, rc);
    TEST_ASSERT_EQUAL_INT(1, opener_calls);
    TEST_ASSERT_NULL(tracker.last_outctx);
}

void test_failed_chunk_open_keeps_last_outctx(void)
{
    AVIOContext *pb = NULL;
    AVDictionary *options = NULL;

    av_dict_set(&options, "stream_index", "0", 0);
    tracker.last_outctx = &prev_outctx;
    opener_rc = -1;

    int rc = elv_io_open(&format_ctx, &pb, "chunk-stream0-00002.m4s", AVIO_FLAG_WRITE, &options);

    TEST_ASSERT_EQUAL_INT(-1, rc);
    TEST_ASSERT_EQUAL_INT(1, opener_calls);
    TEST_ASSERT_EQUAL_PTR(&prev_outctx, tracker.last_outctx);
    av_dict_free(&options);
}

void test_invalid_fsegment_url_keeps_last_outctx(void)
{
    AVIOContext *pb = NULL;

    tracker.last_outctx = &prev_outctx;

    int rc = elv_io_open(&format_ctx, &pb, "fsegment-bogus-00001.mp4", AVIO_FLAG_WRITE, NULL);

    TEST_ASSERT_EQUAL_INT(-1, rc);
    TEST_ASSERT_EQUAL_INT(0, opener_calls);
    TEST_ASSERT_EQUAL_PTR(&prev_outctx, tracker.last_outctx);
}

/* ---------------------------------------------------------------------------
 * Successful open and close
 * ---------------------------------------------------------------------------*/

void test_segment_open_then_close_tracks_last_outctx(void)
{
    AVIOContext *pb = NULL;

    int rc = elv_io_open(&format_ctx, &pb, "fsegment-video-00001.mp4", AVIO_FLAG_WRITE, NULL);

    TEST_ASSERT_EQUAL_INT(0, rc);
    TEST_ASSERT_NOT_NULL(pb);
    TEST_ASSERT_EQUAL_PTR(opened_outctx, pb->opaque);
    TEST_ASSERT_EQUAL_PTR(opened_outctx, tracker.last_outctx);
    TEST_ASSERT_EQUAL_INT(avpipe_video_fmp4_segment, opened_outctx->type);

    rc = elv_io_close(&format_ctx, pb);

    TEST_ASSERT_EQUAL_INT(0, rc);
    TEST_ASSERT_EQUAL_INT(1, closer_calls);
    TEST_ASSERT_NULL(tracker.last_outctx);
}

int main(void)
{
    elv_logger_open("out", "test_io_open", 1, 1024*1024, elv_log_file);

    UNITY_BEGIN();

    RUN_TEST(test_failed_segment_open_keeps_last_outctx);
    RUN_TEST(test_failed_segment_open_with_no_previous_output);
    RUN_TEST(test_failed_chunk_open_keeps_last_outctx);
    RUN_TEST(test_invalid_fsegment_url_keeps_last_outctx);

    RUN_TEST(test_segment_open_then_close_tracks_last_outctx);

    return UNITY_END();
}
