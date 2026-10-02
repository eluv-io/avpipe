/*
 * test_color_defaults.c
 *
 * Unit tests for dash_synthesize_color_defaults() in avpipe_format.c.
 *
 * We include avpipe_format.c directly so that the static helper functions
 * (color_pri_cat, color_trc_cat, color_spc_cat) are accessible.
 */

#include "unity/unity.h"

#include <string.h>
#include <libavcodec/avcodec.h>

/* Pull in the unit under test, including its static helpers. */
#include "../src/avpipe_format.c"

/* ---------------------------------------------------------------------------
 * Helpers
 * ---------------------------------------------------------------------------*/

/* Build a minimal xcparams_t with the given format string. */
static xcparams_t
make_params(const char *format)
{
    xcparams_t p;
    memset(&p, 0, sizeof(p));
    p.format = (char *)format;
    p.url    = (char *)"test";
    return p;
}

/* Build an AVCodecParameters with the given color fields. */
static AVCodecParameters
make_codecpar(
    enum AVColorRange                    range,
    enum AVColorPrimaries                primaries,
    enum AVColorTransferCharacteristic   trc,
    enum AVColorSpace                    space)
{
    AVCodecParameters cp;
    memset(&cp, 0, sizeof(cp));
    cp.color_range     = range;
    cp.color_primaries = primaries;
    cp.color_trc       = trc;
    cp.color_space     = space;
    return cp;
}

/* ---------------------------------------------------------------------------
 * setUp / tearDown (required by Unity)
 * ---------------------------------------------------------------------------*/

void setUp(void)    {}
void tearDown(void) {}

/* ---------------------------------------------------------------------------
 * Tests: format guard
 * ---------------------------------------------------------------------------*/

void test_no_op_for_non_dash_format(void)
{
    xcparams_t p = make_params("fmp4-segment");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_UNSPECIFIED,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_UNSPECIFIED, cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_UNSPECIFIED, cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_UNSPECIFIED, cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Tests: range guard
 * ---------------------------------------------------------------------------*/

void test_no_op_when_range_unspecified(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_UNSPECIFIED, AVCOL_PRI_UNSPECIFIED,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_UNSPECIFIED, cp.color_primaries);
}

/* ---------------------------------------------------------------------------
 * Tests: all-unspecified → synthesize BT.709
 * ---------------------------------------------------------------------------*/

void test_all_unspecified_synthesizes_bt709(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_UNSPECIFIED,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT709, cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_BT709, cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT709, cp.color_space);
}

void test_all_unspecified_hls_synthesizes_bt709(void)
{
    xcparams_t p = make_params("hls");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_UNSPECIFIED,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT709, cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_BT709, cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT709, cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Tests: all-set → no-op
 * ---------------------------------------------------------------------------*/

void test_all_set_no_op(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_BT709,
        AVCOL_TRC_BT709, AVCOL_SPC_BT709);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT709, cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_BT709, cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT709, cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Tests: partial BT.709 — fill missing fields
 * ---------------------------------------------------------------------------*/

void test_bt709_primaries_only_fills_trc_and_space(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_BT709,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT709, cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_BT709, cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT709, cp.color_space);
}

void test_bt709_trc_only_fills_primaries_and_space(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_UNSPECIFIED,
        AVCOL_TRC_BT709, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT709, cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_BT709, cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT709, cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Tests: partial BT.601 NTSC — fill missing fields
 * ---------------------------------------------------------------------------*/

void test_bt601_ntsc_primaries_only_fills_trc_and_space(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_SMPTE170M,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_SMPTE170M,  cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_SMPTE170M,  cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_SMPTE170M,  cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Tests: partial BT.601 PAL — fill missing fields
 * ---------------------------------------------------------------------------*/

void test_bt601_pal_primaries_only_fills_trc_and_space(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_BT470BG,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT470BG,  cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_GAMMA28,  cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT470BG,  cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Tests: HDR — only fill when TRC is unambiguous
 * ---------------------------------------------------------------------------*/

void test_hdr_hdr10_trc_fills_primaries_and_space(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_UNSPECIFIED,
        AVCOL_TRC_SMPTE2084, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT2020,     cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_SMPTE2084,  cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT2020_NCL, cp.color_space);
}

void test_hdr_hlg_trc_fills_primaries_and_space(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_UNSPECIFIED,
        AVCOL_TRC_ARIB_STD_B67, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT2020,      cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_ARIB_STD_B67, cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_BT2020_NCL,  cp.color_space);
}

void test_hdr_bt2020_primaries_no_trc_no_op(void)
{
    xcparams_t p = make_params("dash");
    /* BT.2020 primaries but TRC unspecified — can't tell HDR10 from HLG */
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_BT2020,
        AVCOL_TRC_UNSPECIFIED, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT2020,      cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_UNSPECIFIED,  cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_UNSPECIFIED,  cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Tests: incoherent metadata — no-op
 * ---------------------------------------------------------------------------*/

void test_incoherent_bt709_primaries_hdr_trc_no_op(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_BT709,
        AVCOL_TRC_SMPTE2084, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    /* Incoherent: BT.709 primaries + HDR TRC → no fields should be changed */
    TEST_ASSERT_EQUAL(AVCOL_PRI_BT709,       cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_SMPTE2084,   cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_UNSPECIFIED, cp.color_space);
}

void test_incoherent_bt601_primaries_bt709_trc_no_op(void)
{
    xcparams_t p = make_params("dash");
    AVCodecParameters cp = make_codecpar(
        AVCOL_RANGE_MPEG, AVCOL_PRI_SMPTE170M,
        AVCOL_TRC_BT709, AVCOL_SPC_UNSPECIFIED);
    dash_synthesize_color_defaults(&p, &cp);
    TEST_ASSERT_EQUAL(AVCOL_PRI_SMPTE170M,  cp.color_primaries);
    TEST_ASSERT_EQUAL(AVCOL_TRC_BT709,      cp.color_trc);
    TEST_ASSERT_EQUAL(AVCOL_SPC_UNSPECIFIED, cp.color_space);
}

/* ---------------------------------------------------------------------------
 * Main
 * ---------------------------------------------------------------------------*/

int main(void)
{
    elv_logger_open("out", "test_color_defaults", 1, 1024*1024, elv_log_file);

    UNITY_BEGIN();

    RUN_TEST(test_no_op_for_non_dash_format);
    RUN_TEST(test_no_op_when_range_unspecified);

    RUN_TEST(test_all_unspecified_synthesizes_bt709);
    RUN_TEST(test_all_unspecified_hls_synthesizes_bt709);
    RUN_TEST(test_all_set_no_op);

    RUN_TEST(test_bt709_primaries_only_fills_trc_and_space);
    RUN_TEST(test_bt709_trc_only_fills_primaries_and_space);

    RUN_TEST(test_bt601_ntsc_primaries_only_fills_trc_and_space);
    RUN_TEST(test_bt601_pal_primaries_only_fills_trc_and_space);

    RUN_TEST(test_hdr_hdr10_trc_fills_primaries_and_space);
    RUN_TEST(test_hdr_hlg_trc_fills_primaries_and_space);
    RUN_TEST(test_hdr_bt2020_primaries_no_trc_no_op);

    RUN_TEST(test_incoherent_bt709_primaries_hdr_trc_no_op);
    RUN_TEST(test_incoherent_bt601_primaries_bt709_trc_no_op);

    return UNITY_END();
}
