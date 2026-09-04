/* Unit tests for HDR - HDR10, Dolby Vision - resolution and encoder setup. */

#include "unity/unity.h"

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/log.h>

#include "../src/avpipe_codec.c"

static const char *valid_master_display =
    "G(8500,39850)B(6550,2300)R(35400,14600)"
    "WP(15635,16450)L(10000000,1)";

void setUp(void)    {}
void tearDown(void) {}

static AVPacketSideData *
new_stream_side_data(
    AVStream *stream,
    enum AVPacketSideDataType type,
    size_t size)
{
    return av_packet_side_data_new(&stream->codecpar->coded_side_data,
        &stream->codecpar->nb_coded_side_data, type, size, 0);
}

static void
add_input_hdr10_metadata(
    AVStream *stream)
{
    AVMasteringDisplayMetadata master_display;
    AVContentLightMetadata max_cll;
    AVPacketSideData *side_data;

    TEST_ASSERT_EQUAL_INT(eav_success,
        parse_master_display(valid_master_display, &master_display));
    side_data = new_stream_side_data(stream,
        AV_PKT_DATA_MASTERING_DISPLAY_METADATA, sizeof(master_display));
    TEST_ASSERT_NOT_NULL(side_data);
    memcpy(side_data->data, &master_display, sizeof(master_display));

    TEST_ASSERT_EQUAL_INT(eav_success, parse_max_cll("1000,300", &max_cll));
    side_data = new_stream_side_data(stream,
        AV_PKT_DATA_CONTENT_LIGHT_LEVEL, sizeof(max_cll));
    TEST_ASSERT_NOT_NULL(side_data);
    memcpy(side_data->data, &max_cll, sizeof(max_cll));
}

void
test_valid_master_display_round_trip(void)
{
    AVMasteringDisplayMetadata metadata;
    char formatted[HDR10_MASTER_DISPLAY_SIZE] = {0};

    TEST_ASSERT_EQUAL_INT(eav_success,
        parse_master_display(valid_master_display, &metadata));
    TEST_ASSERT_TRUE(master_display_metadata_valid(&metadata));
    TEST_ASSERT_EQUAL_INT(eav_success,
        format_master_display(formatted, sizeof(formatted), &metadata));
    TEST_ASSERT_EQUAL_STRING(valid_master_display, formatted);
}

void
test_all_zero_master_display_is_rejected(void)
{
    AVMasteringDisplayMetadata metadata;

    TEST_ASSERT_EQUAL_INT(eav_param,
        parse_master_display("G(0,0)B(0,0)R(0,0)WP(0,0)L(0,0)", &metadata));
    TEST_ASSERT_FALSE(master_display_metadata_valid(&metadata));
}

void
test_content_light_validation(void)
{
    AVContentLightMetadata metadata;

    TEST_ASSERT_EQUAL_INT(eav_success, parse_max_cll("1000,300", &metadata));
    TEST_ASSERT_TRUE(content_light_metadata_valid(&metadata));
    TEST_ASSERT_EQUAL_INT(eav_param, parse_max_cll("0,0", &metadata));
    TEST_ASSERT_EQUAL_INT(eav_param, parse_max_cll("300,1000", &metadata));
}

void
test_input_hdr10_is_resolved_and_attached_for_nvenc(void)
{
    AVFormatContext *format = avformat_alloc_context();
    AVStream *stream;
    AVCodecContext *encoder = avcodec_alloc_context3(NULL);
    xcparams_t params = {0};
    hdr10_metadata_t metadata;

    TEST_ASSERT_NOT_NULL(format);
    TEST_ASSERT_NOT_NULL(encoder);
    stream = avformat_new_stream(format, NULL);
    TEST_ASSERT_NOT_NULL(stream);
    stream->codecpar->color_range = AVCOL_RANGE_MPEG;
    stream->codecpar->color_primaries = AVCOL_PRI_BT2020;
    stream->codecpar->color_trc = AVCOL_TRC_SMPTE2084;
    stream->codecpar->color_space = AVCOL_SPC_BT2020_NCL;
    add_input_hdr10_metadata(stream);

    params.url = "test";
    params.ecodec = "hevc_nvenc";
    params.bitdepth = 8;
    TEST_ASSERT_EQUAL_INT(eav_success,
        resolve_hdr10_metadata(stream, NULL, &params, &metadata));
    TEST_ASSERT_TRUE(metadata.enabled);
    TEST_ASSERT_EQUAL_INT(hdr10_metadata_input, metadata.master_display_source);
    TEST_ASSERT_EQUAL_INT(hdr10_metadata_input, metadata.max_cll_source);

    TEST_ASSERT_EQUAL_INT(eav_success,
        configure_hdr10_encoder_context(encoder, &params, &metadata));
    TEST_ASSERT_EQUAL_INT(10, params.bitdepth);
    TEST_ASSERT_EQUAL_INT(AVCOL_PRI_BT2020, encoder->color_primaries);
    TEST_ASSERT_EQUAL_INT(AVCOL_TRC_SMPTE2084, encoder->color_trc);
    TEST_ASSERT_NOT_NULL(av_packet_side_data_get(encoder->coded_side_data,
        encoder->nb_coded_side_data, AV_PKT_DATA_MASTERING_DISPLAY_METADATA));
    TEST_ASSERT_NOT_NULL(av_packet_side_data_get(encoder->coded_side_data,
        encoder->nb_coded_side_data, AV_PKT_DATA_CONTENT_LIGHT_LEVEL));
    TEST_ASSERT_NOT_NULL(av_frame_side_data_get(encoder->decoded_side_data,
        encoder->nb_decoded_side_data, AV_FRAME_DATA_MASTERING_DISPLAY_METADATA));
    TEST_ASSERT_NOT_NULL(av_frame_side_data_get(encoder->decoded_side_data,
        encoder->nb_decoded_side_data, AV_FRAME_DATA_CONTENT_LIGHT_LEVEL));

    avcodec_free_context(&encoder);
    avformat_free_context(format);
}

void
test_explicit_metadata_overrides_input_and_can_disable_cll(void)
{
    AVFormatContext *format = avformat_alloc_context();
    AVStream *stream;
    xcparams_t params = {0};
    hdr10_metadata_t metadata;

    TEST_ASSERT_NOT_NULL(format);
    stream = avformat_new_stream(format, NULL);
    TEST_ASSERT_NOT_NULL(stream);
    add_input_hdr10_metadata(stream);

    params.url = "test";
    params.ecodec = "hevc_nvenc";
    params.master_display = (char *)valid_master_display;
    params.max_cll = (char *)"0,0";
    TEST_ASSERT_EQUAL_INT(eav_success,
        resolve_hdr10_metadata(stream, NULL, &params, &metadata));
    TEST_ASSERT_TRUE(metadata.enabled);
    TEST_ASSERT_EQUAL_INT(hdr10_metadata_xcparams,
        metadata.master_display_source);
    TEST_ASSERT_EQUAL_INT(hdr10_metadata_disabled, metadata.max_cll_source);
    TEST_ASSERT_EQUAL_STRING("", metadata.max_cll);

    avformat_free_context(format);
}

void
test_hlg_input_does_not_implicitly_enable_hdr10(void)
{
    AVFormatContext *format = avformat_alloc_context();
    AVStream *stream;
    xcparams_t params = {0};
    hdr10_metadata_t metadata;

    TEST_ASSERT_NOT_NULL(format);
    stream = avformat_new_stream(format, NULL);
    TEST_ASSERT_NOT_NULL(stream);
    stream->codecpar->color_trc = AVCOL_TRC_ARIB_STD_B67;
    add_input_hdr10_metadata(stream);
    params.url = "test";

    TEST_ASSERT_EQUAL_INT(eav_success,
        resolve_hdr10_metadata(stream, NULL, &params, &metadata));
    TEST_ASSERT_FALSE(metadata.enabled);

    avformat_free_context(format);
}

void
test_dovi_hdr10_compatibility_id(void)
{
    AVFormatContext *format = avformat_alloc_context();
    AVStream *stream;
    AVPacketSideData *side_data;
    AVDOVIDecoderConfigurationRecord *dovi;

    TEST_ASSERT_NOT_NULL(format);
    stream = avformat_new_stream(format, NULL);
    TEST_ASSERT_NOT_NULL(stream);
    side_data = new_stream_side_data(stream, AV_PKT_DATA_DOVI_CONF,
                                    sizeof(*dovi));
    TEST_ASSERT_NOT_NULL(side_data);
    dovi = (AVDOVIDecoderConfigurationRecord *)side_data->data;
    dovi->bl_present_flag = 1;
    dovi->dv_bl_signal_compatibility_id = 1;

    TEST_ASSERT_TRUE(is_dovi_hdr10_compatible(stream));
    dovi->dv_bl_signal_compatibility_id = 4;
    TEST_ASSERT_FALSE(is_dovi_hdr10_compatible(stream));

    avformat_free_context(format);
}

int
main(void)
{
    av_log_set_level(AV_LOG_ERROR);
    elv_logger_open("out", "test_hdr_metadata", 1, 1024 * 1024, elv_log_file);

    UNITY_BEGIN();
    RUN_TEST(test_valid_master_display_round_trip);
    RUN_TEST(test_all_zero_master_display_is_rejected);
    RUN_TEST(test_content_light_validation);
    RUN_TEST(test_input_hdr10_is_resolved_and_attached_for_nvenc);
    RUN_TEST(test_explicit_metadata_overrides_input_and_can_disable_cll);
    RUN_TEST(test_hlg_input_does_not_implicitly_enable_hdr10);
    RUN_TEST(test_dovi_hdr10_compatibility_id);
    return UNITY_END();
}
