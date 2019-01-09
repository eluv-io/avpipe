/*
 * Test a/v transcoding pipeline
 *
 * Build:
 *
 * ELV_TOOLCHAIN_DIST_PLATFORM=...
 * gcc -Wall -L $ELV_TOOLCHAIN_DIST_PLATFORM/lib -I $ELV_TOOLCHAIN_DIST_PLATFORM/include/ elv_xc_test.c -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -o tx
 *
 *
 */

#include <libavutil/log.h>

#include "elv_xc.h"
#include "elv_log.h"

/*
 * Test basic decoding and encoding
 *
 * Usage: <FILE-IN> <FILE-OUT>
 */
int
main(
    int argc,
    char *argv[])
{
    txctx_t *txctx;

    /* Parameters */
    txparams_t p = {
        .video_bitrate = 2560000,           /* not used if using CRF */
        .audio_bitrate = 64000,
        .sample_rate = 44100,               /* Audio sampling rate */
        .crf_str = "23",                    /* 1 best -> 23 standard middle -> 52 poor */
        .start_time_ts = 0,                 /* same units as input stream PTS */
        //.duration_ts = 1001 * 60 * 12,      /* same units as input stream PTS */
        .duration_ts = -1,                  /* -1 means entire input stream */
        .start_segment_str = "1",           /* 1-based */
        .seg_duration_ts = 1001 * 60,       /* same units as input stream PTS */
        .seg_duration_fr = 60,              /* in frames-per-secoond units */
        .seg_duration_secs_str = "2.002",
        .codec = "libx264",
        .enc_height = 720,                  /* -1 means use source height, other values 2160, 720 */
        .enc_width = 1280                   /* -1 means use source width, other values 3840, 1280 */
    };

    // Set AV libs log level
    //av_log_set_level(AV_LOG_DEBUG);

    if ( argc == 1 ) {
        printf("Usage: %s <in-filename> <out-filename>\nNeed to pass input and output filenames\n", argv[0]);
        return -1;
    }

    elv_logger_open(NULL, "etx", 10, 10*1024*1024, elv_log_file);
    elv_set_log_level(elv_log_log);

    if (tx_init(&txctx, argv[1], argv[2], &p) < 0)
        return 1;

    if (tx(txctx, 0) < 0) {
        elv_err("Error in transcoding");
        return -1;
    }

    elv_dbg("Releasing all the resources");
    tx_fini(&txctx);

    return 0;
}
