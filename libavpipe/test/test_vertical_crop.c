/*
 * test_vertical_crop.c
 *
 * Verifies that the vertical (9:16) crop window follows the per-frame
 * vertical_data, frame by frame - from the in-memory buffer and from a streaming
 * reader (vertical_data_reader), including the hold once the stream ends.
 *
 * Rather than transcoding a fixture, this drives the production filter path
 * directly on synthesized frames:
 *
 *   synthesized gradient AVFrame  ->  init_video_filters()   (production)
 *                                 ->  crop_get_context()     (production)
 *                                 ->  crop_send_command()    (production, per frame)
 *                                 ->  av_buffersink_get_frame()
 *                                 ->  mean luma of data[0]
 *
 * The source is a static horizontal luma ramp, Y(x) = 16 + 219*x/(W-1) with flat
 * chroma, so the mean luma of a cropped window reports where the crop landed:
 * for a linear ramp the window mean equals the value at the window's midpoint.
 * Because nothing is encoded or decoded, the pixels are exactly the formula -
 * no compression noise and no limited/full range conversion to model.
 *
 * Geometry (1920x1080 source, encoder height 360):
 *
 *   scale=-2:360            1920x1080 -> 640x360     (scaled_width 640)
 *   crop_calc_width(360)    = 360*9/16 = 202         (W, fixed)
 *   crop=202:ih:x:0         x from vertical_data, y pinned, full height
 *   crop_x range            [0, 640-202] = [0, 438]
 *
 * The 1920x1080 source is deliberate: it makes the scale step do real work, so
 * the scaled_width arithmetic in crop_send_command() (dec.width * enc_height /
 * dec.height) is actually exercised rather than being a no-op identity.
 */

#include "unity/unity.h"

#include <math.h>
#include <stdint.h>
#include <string.h>
#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/imgutils.h>
#include <libavutil/intreadwrite.h>
#include <libavutil/rational.h>

/* Units under test, plus the real vertical_data_crop_x(). */
#include "../src/avpipe_filters.c"
#include "../src/avpipe_utils.c"

/* Only external symbol avpipe_utils.c needs; it lives in avpipe_xc.c, which we
 * do not want to drag in. Unreferenced by these tests. */
const char *
avpipe_channel_name(int nb_channels, int channel_layout)
{
    (void)nb_channels; (void)channel_layout;
    return "";
}

void setUp(void)    {}
void tearDown(void) {}

#define SRC_W       1920
#define SRC_H       1080
#define ENC_H        360
#define SCALED_W     (SRC_W * ENC_H / SRC_H)    /* 640 */
#define N_FRAMES     180
#define LUMA_MIN      16
#define LUMA_SPAN    219                        /* 16..235, limited range */

/* ---------------------------------------------------------------------------
 * Source pattern
 * ---------------------------------------------------------------------------*/

/* Luma the ramp carries at normalized position p (0..1). Resolution-independent,
 * so it applies equally to the source and to the scaled frame. */
static double
ramp_luma(double p)
{
    return LUMA_MIN + LUMA_SPAN * p;
}

static AVFrame *
make_gradient_frame(void)
{
    AVFrame *f = av_frame_alloc();
    if (!f)
        return NULL;
    f->format = AV_PIX_FMT_YUV420P;
    f->width  = SRC_W;
    f->height = SRC_H;
    if (av_frame_get_buffer(f, 32) < 0) {
        av_frame_free(&f);
        return NULL;
    }
    for (int y = 0; y < SRC_H; y++) {
        uint8_t *row = f->data[0] + (ptrdiff_t)y * f->linesize[0];
        for (int x = 0; x < SRC_W; x++)
            row[x] = (uint8_t)(LUMA_MIN + (LUMA_SPAN * x) / (SRC_W - 1));
    }
    for (int p = 1; p <= 2; p++)
        for (int y = 0; y < SRC_H / 2; y++)
            memset(f->data[p] + (ptrdiff_t)y * f->linesize[p], 128, SRC_W / 2);
    return f;
}

static double
mean_luma(const AVFrame *f)
{
    int64_t sum = 0;
    for (int y = 0; y < f->height; y++) {
        const uint8_t *row = f->data[0] + (ptrdiff_t)y * f->linesize[0];
        for (int x = 0; x < f->width; x++)
            sum += row[x];
    }
    return (double)sum / ((double)f->width * f->height);
}

/* ---------------------------------------------------------------------------
 * Pan pattern: a triangle wave phase-shifted a quarter cycle, so each cycle
 * starts and ends at centre - 0.5 -> 1 -> 0 -> 0.5 - repeating every PAN_PERIOD
 * frames. Continuous across the wrap, so the window moves at a constant speed
 * throughout and the residual below has no pattern-induced steps in it.
 *
 * Kept in sync with the "pingpong" case in scripts/gen-vertical-data.sh, which
 * writes this same pattern as a vertical_data file for visual inspection with
 * exc on real footage.
 *
 * Constant speed means a crop lagging the data by a
 * frame lands a fixed distance off the expected position on every frame, and the
 * sign of that offset flips at each turnaround, so it shows up as residual
 * spread rather than cancelling into the mean.
 *
 * Values are kept in [PAN_LO,PAN_HI], inside the frame-edge clamp, so the window
 * always tracks v exactly and the expected position stays an exact function of v.
 * Were the range to reach the clamp, both the crop and the expected position
 * would saturate together and the check would still pass while measuring nothing.
 * ---------------------------------------------------------------------------*/
#define PAN_PERIOD 60   /* frames per centre -> right -> left -> centre cycle */
#define PAN_LO     (VERTICAL_DATA_SCALE * 16 / 100)   /* 0.16 - just inside the left clamp  */
#define PAN_HI     (VERTICAL_DATA_SCALE * 84 / 100)   /* 0.84 - just inside the right clamp */

static uint32_t
pan_value(int n)
{
    double p = fmod((double)n / PAN_PERIOD + 0.25, 1.0);
    double c = 1 - fabs(1 - 2 * p);
    return (uint32_t)(PAN_LO + (int)((PAN_HI - PAN_LO) * c + 0.5));
}

/* ---------------------------------------------------------------------------
 * Fixture: minimal decoder/encoder contexts, enough for init_video_filters()
 * and crop_send_command().
 * ---------------------------------------------------------------------------*/
typedef struct crop_fixture_t {
    coderctx_t      decoder;
    coderctx_t      encoder;
    AVCodecContext  dec_codec;
    AVCodecContext  enc_codec;
    xcparams_t      params;
    uint8_t        *vdata;
} crop_fixture_t;

static void
fixture_init(crop_fixture_t *f)
{
    memset(f, 0, sizeof(*f));

    f->dec_codec.width               = SRC_W;
    f->dec_codec.height              = SRC_H;
    f->dec_codec.pix_fmt             = AV_PIX_FMT_YUV420P;
    f->dec_codec.sample_aspect_ratio = (AVRational){ 1, 1 };
    f->dec_codec.time_base           = (AVRational){ 1, 30000 };

    f->enc_codec.width     = crop_calc_width(ENC_H);
    f->enc_codec.height    = ENC_H;
    f->enc_codec.pix_fmt   = AV_PIX_FMT_YUV420P;
    f->enc_codec.time_base = (AVRational){ 1, 30000 };

    f->decoder.video_stream_index    = 0;
    f->decoder.codec_context[0]      = &f->dec_codec;
    f->decoder.video_colorspace      = AVCOL_SPC_BT709;
    f->decoder.video_color_range     = AVCOL_RANGE_MPEG;

    f->encoder.video_stream_index = 0;
    f->encoder.codec_context[0]   = &f->enc_codec;

    f->params.start_time_ts = 0;
    f->params.vertical      = vertical_32bpf;

    f->vdata = calloc(N_FRAMES, 4);
    for (int n = 0; n < N_FRAMES; n++)
        AV_WL32(f->vdata + n * 4, pan_value(n));
    f->params.vertical_data     = f->vdata;
    f->params.vertical_data_len = N_FRAMES * 4;
}

static void
fixture_free(crop_fixture_t *f)
{
    if (f->decoder.video_filter_graph)
        avfilter_graph_free(&f->decoder.video_filter_graph);
    free(f->vdata);
}

/* ---------------------------------------------------------------------------
 * Tests
 * ---------------------------------------------------------------------------*/

/* The crop width is derived purely from the encoder height via 9:16 - the
 * per-frame value never affects it.
 *
 * crop_calc_width() truncates (integer division), then bumps an odd result up to
 * even for codec compatibility. It is not a round-to-nearest: both cases below
 * discard an identical .5, and only the parity of the truncated value decides
 * the outcome. */
void
test_crop_width_is_9_16_of_height(void)
{
    TEST_ASSERT_EQUAL_INT(202, crop_calc_width(360));   /* 360 * 9/16 = 202.5 -> trunc 202, even, kept */
    TEST_ASSERT_EQUAL_INT(608, crop_calc_width(1080));  /* 1080 * 9/16 = 607.5 -> trunc 607, odd,  +1   */
    TEST_ASSERT_EQUAL_INT(0, crop_calc_width(360) % 2); /* always even */
    TEST_ASSERT_EQUAL_INT(0, crop_calc_width(1080) % 2);
}

/* Position the crop should land on for frame n, given the fixture's data source. */
typedef int (*expected_crop_x_f)(crop_fixture_t *f, int n, int crop_w);

/* Push the gradient through the real filter path once per frame, updating the
 * crop via crop_send_command(), and collect the per-frame residual between the
 * measured mean luma and the ramp value at the expected window's midpoint.
 * Returns the number of frames measured. */
static int
measure_tracking(
    crop_fixture_t *f,
    expected_crop_x_f expected_crop_x,
    double *mean_out,
    double *std_out)
{
    char filter_str[256];
    AVFrame *src = NULL, *filt = NULL;
    double residuals[N_FRAMES];
    int n_checked = 0;
    const int crop_w = crop_calc_width(ENC_H);

    /* Mirrors the vertical branch of get_filter_str(). That function is static in
     * avpipe_xc.c, so the template is duplicated here - keep the two in sync. */
    snprintf(filter_str, sizeof(filter_str), "scale=-2:%d,crop=%d:ih:200:0",
        ENC_H, crop_w);

    TEST_ASSERT_EQUAL_INT(0,
        init_video_filters(filter_str, &f->decoder, &f->encoder, &f->params));
    TEST_ASSERT_EQUAL_INT(0, crop_get_context(&f->decoder, &f->params));

    src  = make_gradient_frame();
    filt = av_frame_alloc();
    TEST_ASSERT_NOT_NULL(src);
    TEST_ASSERT_NOT_NULL(filt);

    for (int n = 0; n < N_FRAMES; n++) {
        /* The decoder's 1-based frame counter is what crop_send_command() reads. */
        f->dec_codec.frame_num = n + 1;
        TEST_ASSERT_EQUAL_INT(eav_success,
            crop_send_command(&f->decoder, &f->encoder, &f->params));

        src->pts = n;
        TEST_ASSERT_TRUE(av_buffersrc_add_frame_flags(
            f->decoder.video_buffersrc_ctx, src, AV_BUFFERSRC_FLAG_KEEP_REF) >= 0);

        int ret = av_buffersink_get_frame(f->decoder.video_buffersink_ctx, filt);
        if (ret == AVERROR(EAGAIN))
            continue;
        TEST_ASSERT_TRUE(ret >= 0);

        TEST_ASSERT_EQUAL_INT(crop_w, filt->width);
        TEST_ASSERT_EQUAL_INT(ENC_H, filt->height);   /* full height, never cropped */

        /* Expected: the ramp value at the crop window's midpoint. */
        double mid  = expected_crop_x(f, n, crop_w) + crop_w / 2.0;
        double want = ramp_luma(mid / (SCALED_W - 1));

        residuals[n_checked++] = mean_luma(filt) - want;
        av_frame_unref(filt);
    }

    TEST_ASSERT_GREATER_THAN_INT(N_FRAMES / 2, n_checked);

    double mean = 0, var = 0;
    for (int i = 0; i < n_checked; i++)
        mean += residuals[i];
    mean /= n_checked;
    for (int i = 0; i < n_checked; i++)
        var += (residuals[i] - mean) * (residuals[i] - mean);
    *mean_out = mean;
    *std_out  = sqrt(var / n_checked);

    av_frame_free(&src);
    av_frame_free(&filt);
    return n_checked;
}

static int
expected_from_buffer(crop_fixture_t *f, int n, int crop_w)
{
    return vertical_data_crop_x(f->params.vertical_data, f->params.vertical_data_len,
        n, SCALED_W, crop_w);
}

/* Check the cropped output lands where the frame's vertical_data value asked for. */
void
test_crop_tracks_vertical_data_per_frame(void)
{
    crop_fixture_t f;
    double mean, std;

    fixture_init(&f);
    int n_checked = measure_tracking(&f, expected_from_buffer, &mean, &std);

    printf("  crop-tracking residual: mean=%.3f stddev=%.3f over %d frames\n",
        mean, std, n_checked);

    /* Uncompressed, so the only error is scaler interpolation and integer
     * rounding in the ramp. A stuck or lagging crop would swing this wildly:
     * the pan sweeps the window across the full gradient. */
    TEST_ASSERT_TRUE(std < 2.0);
    TEST_ASSERT_TRUE(fabs(mean) < 2.0);

    fixture_free(&f);
}

/* ---------------------------------------------------------------------------
 * Streaming source: stands in for the Go reader behind vertical_data_reader,
 * serving the same pan pattern one record per call and reporting EOF after
 * STREAM_RECORDS of them, so the tail of the run exercises the hold.
 * ---------------------------------------------------------------------------*/
#define STREAM_RECORDS 120   /* < N_FRAMES: the remaining frames must hold the last value */

typedef struct stream_source_t {
    int next;    /* index of the next record to serve */
    int calls;   /* how many times the reader was invoked */
} stream_source_t;

static int
stream_read(uintptr_t handle, uint32_t *value)
{
    stream_source_t *s = (stream_source_t *)handle;
    s->calls++;
    if (s->next >= STREAM_RECORDS)
        return 0;   /* EOF */
    *value = pan_value(s->next++);
    return 1;
}

static int
expected_from_stream(crop_fixture_t *f, int n, int crop_w)
{
    (void)f;
    /* Frames past the end of the stream keep the last value served. */
    int last = n < STREAM_RECORDS ? n : STREAM_RECORDS - 1;
    return vertical_value_crop_x(pan_value(last), SCALED_W, crop_w);
}

/* The same measurement over the streaming branch of crop_send_command(): one
 * record consumed per frame, and the last value held after EOF. */
void
test_crop_tracks_streamed_vertical_data(void)
{
    crop_fixture_t f;
    stream_source_t source = { 0, 0 };
    double mean, std;

    fixture_init(&f);
    /* Swap the buffer for the reader: with both set crop_send_command() uses the buffer. */
    f.params.vertical_data               = NULL;
    f.params.vertical_data_len           = 0;
    f.params.vertical_data_reader        = stream_read;
    f.params.vertical_data_reader_handle = (uintptr_t)&source;

    int n_checked = measure_tracking(&f, expected_from_stream, &mean, &std);

    printf("  streamed crop-tracking residual: mean=%.3f stddev=%.3f over %d frames\n",
        mean, std, n_checked);

    TEST_ASSERT_TRUE(std < 2.0);
    TEST_ASSERT_TRUE(fabs(mean) < 2.0);

    /* Cadence: exactly one read per frame until the EOF read, and none after it. */
    TEST_ASSERT_EQUAL_INT(STREAM_RECORDS + 1, source.calls);
    TEST_ASSERT_EQUAL_INT(1, f.decoder.vertical_data_eof);
    TEST_ASSERT_EQUAL_INT(1, f.decoder.vertical_data_has_last);
    TEST_ASSERT_EQUAL_UINT32(pan_value(STREAM_RECORDS - 1), f.decoder.vertical_data_last);

    fixture_free(&f);
}

int
main(void)
{
    av_log_set_level(AV_LOG_ERROR);
    elv_logger_open("out", "test_vertical_crop", 1, 1024 * 1024, elv_log_file);

    UNITY_BEGIN();

    RUN_TEST(test_crop_width_is_9_16_of_height);
    RUN_TEST(test_crop_tracks_vertical_data_per_frame);
    RUN_TEST(test_crop_tracks_streamed_vertical_data);

    return UNITY_END();
}
