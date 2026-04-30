/*
 * mvhevc_xc.h - MV-HEVC transcoding API
 *
 * Provides init/xc/fini pattern modeled after avpipe_xc.h for
 * multiview HEVC encoding using x265.
 *
 * Supports two input modes:
 *   - Two separate files (left eye + right eye)
 *   - Single MV-HEVC file (both views decoded via FFmpeg view_ids)
 */

#ifndef MVHEVC_XC_H
#define MVHEVC_XC_H

#include <stdio.h>
#include <libavformat/avformat.h>
#include <libavcodec/avcodec.h>
#include <libswscale/swscale.h>
#include <x265.h>

#include "avpipe_xc.h"

/* ------------------------------------------------------------------ */
/* MV-HEVC extra parameters not in xcparams_t                         */
/* ------------------------------------------------------------------ */
typedef struct mvhevc_params {
    int         scenecut;       /* Scene-cut sensitivity (0 = disable, default 40) */
    int         bframes;        /* Max consecutive B-frames (-1 = use preset default) */
    int         high_tier;      /* 0 = Main tier (default), 1 = High tier */
    const char *tune;           /* x265 tune: psnr, ssim, grain, etc. (NULL = none) */
    int         fps_num;        /* Framerate override (0 = detect from input) */
    int         fps_den;
} mvhevc_params;

/* ------------------------------------------------------------------ */
/* Input decoder context (one per eye)                                */
/* ------------------------------------------------------------------ */
typedef struct mvhevc_input {
    AVFormatContext *fmt_ctx;
    AVCodecContext  *dec_ctx;
    int              video_idx;
    struct SwsContext *sws_ctx;
    AVFrame         *frame;
    AVFrame         *frame420;
    AVPacket        *pkt;
    int              width;
    int              height;
    int              eof;
} mvhevc_input;

/* ------------------------------------------------------------------ */
/* MV-HEVC transcoding context                                        */
/* ------------------------------------------------------------------ */
typedef struct mvhevc_ctx {
    xcparams_t      *params;        /* Encoding parameters (owned, freed by fini) */
    mvhevc_params    mv;            /* MV-HEVC specific parameters */

    int              single_input;  /* 1 = single MV-HEVC file, 0 = two files */
    mvhevc_input     left;          /* Left eye decoder (or single MV-HEVC input) */
    mvhevc_input     right;         /* Right eye decoder (unused in single mode) */
    AVFrame         *right_frame420; /* Right eye converted frame (single mode only) */

    const x265_api  *api;           /* x265 API handle */
    x265_encoder    *encoder;       /* x265 encoder instance */
    x265_param      *x265_params;   /* x265 encoder parameters */

    FILE            *fp_out;        /* Output file handle */
    int64_t          frame_count;   /* Frames encoded so far */
    int              fps_num;       /* Effective framerate numerator */
    int              fps_den;       /* Effective framerate denominator */
} mvhevc_ctx;

/* ------------------------------------------------------------------ */
/* API functions                                                       */
/* ------------------------------------------------------------------ */

/* Set mvhevc_params to defaults. */
void mvhevc_params_defaults(mvhevc_params *p);

/* Validate xcparams_t for MV-HEVC: returns 0 on success, -1 on error. */
int mvhevc_validate_params(const xcparams_t *xc);

/*
 * mvhevc_init - Initialize MV-HEVC transcoding context.
 *
 * Two input modes:
 *   - Two files: pass left_file and right_file (separate eye inputs)
 *   - Single file: pass left_file as the MV-HEVC input, right_file = NULL
 *     (decoder extracts both views via FFmpeg view_ids option)
 *
 * Returns 0 on success, negative on error.
 * On error, caller must still call mvhevc_fini() to release partial resources.
 */
int mvhevc_init(mvhevc_ctx **ctx,
                xcparams_t *xc,
                mvhevc_params *mv,
                const char *left_file,
                const char *right_file,
                const char *out_file);

/*
 * mvhevc_xc - Run the MV-HEVC encode loop.
 *
 * Decodes frames from both inputs, encodes as multiview HEVC, writes output.
 * Returns 0 on success, negative on error.
 */
int mvhevc_xc(mvhevc_ctx *ctx);

/*
 * mvhevc_fini - Release all resources.
 *
 * Closes encoder, decoders, output file, frees context.
 * Sets *ctx to NULL. Safe to call with NULL.
 */
void mvhevc_fini(mvhevc_ctx **ctx);

#endif /* MVHEVC_XC_H */
