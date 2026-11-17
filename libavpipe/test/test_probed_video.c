/* Post-probe nominal cadence validation, without an encoder or live socket. */
#include "unity/unity.h"
#include "../src/avpipe_format.c"

static coderctx_t decoder;
static xcparams_t params;
static AVStream *video;

void setUp(void)
{
    memset(&decoder, 0, sizeof(decoder));
    memset(&params, 0, sizeof(params));
    params.url = "test-probed-video";
    params.xc_type = xc_video;
    decoder.format_context = avformat_alloc_context();
    TEST_ASSERT_NOT_NULL(decoder.format_context);
    /* Verify that validation uses the selected index, not stream zero. */
    AVStream *audio = avformat_new_stream(decoder.format_context, NULL);
    TEST_ASSERT_NOT_NULL(audio);
    audio->codecpar->codec_type = AVMEDIA_TYPE_AUDIO;
    video = avformat_new_stream(decoder.format_context, NULL);
    TEST_ASSERT_NOT_NULL(video);
    video->codecpar->codec_type = AVMEDIA_TYPE_VIDEO;
    video->r_frame_rate = (AVRational){50, 1};
    video->avg_frame_rate = (AVRational){50, 1};
    video->time_base = (AVRational){1, 90000};
    decoder.video_stream_index = video->index;
    decoder.live_proto = avp_proto_mpegts;
    decoder.live_container = avp_container_mpegts;
    decoder.codec_context[video->index] = avcodec_alloc_context3(NULL);
    TEST_ASSERT_NOT_NULL(decoder.codec_context[video->index]);
    decoder.codec_context[video->index]->framerate = (AVRational){50, 1};
}

void tearDown(void)
{
    avcodec_free_context(&decoder.codec_context[video->index]);
    avformat_free_context(decoder.format_context);
}

static void assert_rejected(void)
{
    TEST_ASSERT_EQUAL_INT(eav_stream_info, validate_probed_video_stream(&decoder, &params));
    TEST_ASSERT_EQUAL_INT(0, decoder.nominal_frame_rate.num);
}

static void test_supported_live_rates_are_exact(void)
{
    const AVRational rates[] = {
        {24000, 1001}, {24, 1}, {25, 1}, {30000, 1001}, {30, 1},
        {50, 1}, {60000, 1001}, {60, 1}
    };
    for (unsigned int i = 0; i < sizeof(rates) / sizeof(rates[0]); i++) {
        video->r_frame_rate = rates[i];
        decoder.codec_context[video->index]->framerate = rates[i];
        TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, &params));
        TEST_ASSERT_EQUAL_INT(rates[i].num, decoder.nominal_frame_rate.num);
        TEST_ASSERT_EQUAL_INT(rates[i].den, decoder.nominal_frame_rate.den);
    }
}

static void test_missing_frames_do_not_lower_nominal_rate(void)
{
    video->avg_frame_rate = (AVRational){130, 3};
    TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, &params));
    TEST_ASSERT_EQUAL_INT(50, decoder.nominal_frame_rate.num);
    TEST_ASSERT_EQUAL_INT(1, decoder.nominal_frame_rate.den);
    TEST_ASSERT_EQUAL_INT(130, video->avg_frame_rate.num);
    TEST_ASSERT_EQUAL_INT(3, video->avg_frame_rate.den);
}

static void test_mezzanine_timing_clock_is_not_used_as_fps(void)
{
    decoder.live_proto = avp_proto_none;
    decoder.live_container = avp_container_none;
    video->avg_frame_rate = (AVRational){130, 3};
    decoder.codec_context[video->index]->framerate = (AVRational){90000, 1};
    TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, &params));
    TEST_ASSERT_EQUAL_INT(50, decoder.nominal_frame_rate.num);
    /* Calculation only: no mutation of the decoder or stream fields. */
    TEST_ASSERT_EQUAL_INT(90000, decoder.codec_context[video->index]->framerate.num);
    TEST_ASSERT_EQUAL_INT(50, video->r_frame_rate.num);
    TEST_ASSERT_EQUAL_INT(130, video->avg_frame_rate.num);
    TEST_ASSERT_EQUAL_INT(90000, video->time_base.den);
}

static void test_live_rejects_unsupported_and_rounded_rates(void)
{
    const AVRational rates[] = {{27, 1}, {2997, 100}, {5997, 100}, {90000, 1}};
    for (unsigned int i = 0; i < sizeof(rates) / sizeof(rates[0]); i++) {
        video->r_frame_rate = rates[i];
        decoder.codec_context[video->index]->framerate = rates[i];
        assert_rejected();
    }
}

static void test_live_rejects_missing_or_conflicting_evidence(void)
{
    decoder.codec_context[video->index]->framerate = (AVRational){0, 1};
    assert_rejected();
    decoder.codec_context[video->index]->framerate = (AVRational){30, 1};
    assert_rejected();
    decoder.codec_context[video->index]->framerate = (AVRational){90000, 1};
    assert_rejected();
}

static void test_missing_nominal_does_not_fall_back_to_average(void)
{
    video->r_frame_rate = (AVRational){0, 1};
    assert_rejected();
    video->r_frame_rate = (AVRational){50, 0};
    assert_rejected();
    video->r_frame_rate = (AVRational){-50, 1};
    assert_rejected();
}

static void test_live_ts_requires_90khz_but_rtmp_does_not(void)
{
    video->time_base = (AVRational){1, 1000};
    assert_rejected();
    decoder.live_proto = avp_proto_rtmp;
    decoder.live_container = avp_container_flv;
    TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, &params));
    decoder.live_proto = avp_proto_srt;
    decoder.live_container = avp_container_mpegts;
    assert_rejected();
    decoder.live_proto = avp_proto_rtp;
    assert_rejected();
}

static void test_nonlive_other_rates_and_timebases_remain_supported(void)
{
    decoder.live_proto = avp_proto_none;
    decoder.live_container = avp_container_none;
    video->r_frame_rate = (AVRational){25, 1};
    video->time_base = (AVRational){1, 12800};
    TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, &params));
    TEST_ASSERT_EQUAL_INT(25, decoder.nominal_frame_rate.num);
}

static void test_invalid_stream_or_timebase_fails(void)
{
    decoder.video_stream_index = -1;
    assert_rejected();
    decoder.video_stream_index = video->index;
    video->time_base = (AVRational){0, 1};
    assert_rejected();
}

static void test_audio_and_probe_only_do_not_require_video_rate(void)
{
    video->r_frame_rate = (AVRational){0, 1};
    params.xc_type = xc_audio;
    TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, &params));
    TEST_ASSERT_EQUAL_INT(0, decoder.nominal_frame_rate.num);
    TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, NULL));
}

static void test_equivalent_rationals_are_normalized(void)
{
    video->r_frame_rate = (AVRational){100, 2};
    video->time_base = (AVRational){2, 180000};
    TEST_ASSERT_EQUAL_INT(eav_success, validate_probed_video_stream(&decoder, &params));
    TEST_ASSERT_EQUAL_INT(50, decoder.nominal_frame_rate.num);
    TEST_ASSERT_EQUAL_INT(1, decoder.nominal_frame_rate.den);
}

int main(void)
{
    UNITY_BEGIN();
    RUN_TEST(test_supported_live_rates_are_exact);
    RUN_TEST(test_missing_frames_do_not_lower_nominal_rate);
    RUN_TEST(test_mezzanine_timing_clock_is_not_used_as_fps);
    RUN_TEST(test_live_rejects_unsupported_and_rounded_rates);
    RUN_TEST(test_live_rejects_missing_or_conflicting_evidence);
    RUN_TEST(test_missing_nominal_does_not_fall_back_to_average);
    RUN_TEST(test_live_ts_requires_90khz_but_rtmp_does_not);
    RUN_TEST(test_nonlive_other_rates_and_timebases_remain_supported);
    RUN_TEST(test_invalid_stream_or_timebase_fails);
    RUN_TEST(test_audio_and_probe_only_do_not_require_video_rate);
    RUN_TEST(test_equivalent_rationals_are_normalized);
    return UNITY_END();
}
