/*
 * mvhevc_encoder.c - CLI for MV-HEVC encoding
 *
 * Thin command-line wrapper around mvhevc_xc.h API.
 * Parses arguments into xcparams_t + mvhevc_params, then calls
 * mvhevc_init() / mvhevc_xc() / mvhevc_fini().
 *
 * Modeled after elv_xc.c but simplified for MV-HEVC only.
 *
 * Usage:
 *   mvhevc_encoder [options] <left_eye> <right_eye> <output.hevc>
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "mvhevc_xc.h"

static void usage(const char *prog)
{
    fprintf(stderr,
        "Usage: %s [options] <left_eye> <right_eye> <output.hevc>\n"
        "       %s [options] -i <mvhevc_input> <output.hevc>\n"
        "\n"
        "Encodes MV-HEVC using x265 multiview encoding.\n"
        "\n"
        "Input modes:\n"
        "  Two files:   <left_eye> <right_eye>  (separate eye inputs)\n"
        "  Single file: -i <input.mp4>          (existing MV-HEVC, re-encode)\n"
        "\n"
        "Options:\n"
        "  -crf <val>          CRF quality (default 23, lower = better)\n"
        "  -bitrate <kbps>     Target bitrate (0 = use CRF mode)\n"
        "  -maxrate <kbps>     VBV max bitrate (enables VBV)\n"
        "  -bufsize <kbits>    VBV buffer size\n"
        "  -w <pixels>         Output width\n"
        "  -h <pixels>         Output height (width auto-computed if -w omitted)\n"
        "  -keyint <frames>    Force IDR interval (default 0 = auto)\n"
        "  -bframes <n>        Max consecutive B-frames (-1 = preset default)\n"
        "  -preset <name>      x265 preset (default: medium)\n"
        "  -tune <name>        x265 tune (e.g. grain, psnr, ssim)\n"
        "  -profile <name>     HEVC profile (e.g. main, main10)\n"
        "  -level <val>        Level * 10, e.g. 51 = level 5.1\n"
        "  -hightier           Use High tier (default: Main)\n"
        "  -bitdepth <n>       Bit depth: 8 or 10 (default 8)\n"
        "  -scenecut <n>       Scene-cut sensitivity (default 40, 0 = disable)\n"
        "  -fps <num/den>      Override framerate (e.g. 24000/1001)\n"
        "  -max-cll <val>      HDR MaxCLL (e.g. \"1000,200\")\n"
        "  -master-display <v> HDR master display string\n"
        "\n"
        "Then inject spatial metadata and mux to MP4:\n"
        "  MP4Box -add output.hevc:fps=23.976 -new output_muxed.mp4\n"
        "  inject_spatial output_muxed.mp4 output_spatial.mp4\n",
        prog, prog);
}

int main(int argc, char *argv[])
{
    if (argc < 3) {
        usage(argv[0]);
        return 1;
    }

    /* Initialize parameters */
    xcparams_t xc;
    memset(&xc, 0, sizeof(xc));
    xc.preset   = "medium";
    xc.bitdepth = 8;
    xc.crf_str  = "23";

    mvhevc_params mv;
    mvhevc_params_defaults(&mv);

    char crf_buf[16] = "23";
    const char *single_input = NULL;  /* -i mode: single MV-HEVC input */

    /* Parse options */
    int argi = 1;
    while (argi < argc && argv[argi][0] == '-') {
        const char *opt = argv[argi++];
        if (!strcmp(opt, "-i") && argi < argc) {
            single_input = argv[argi++];
        } else if (!strcmp(opt, "-crf") && argi < argc) {
            snprintf(crf_buf, sizeof(crf_buf), "%s", argv[argi++]);
            xc.crf_str = crf_buf;
        } else if (!strcmp(opt, "-bitrate") && argi < argc) {
            xc.video_bitrate = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-maxrate") && argi < argc) {
            xc.rc_max_rate = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-bufsize") && argi < argc) {
            xc.rc_buffer_size = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-w") && argi < argc) {
            xc.enc_width = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-h") && argi < argc) {
            xc.enc_height = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-keyint") && argi < argc) {
            xc.force_keyint = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-bframes") && argi < argc) {
            mv.bframes = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-preset") && argi < argc) {
            xc.preset = argv[argi++];
        } else if (!strcmp(opt, "-tune") && argi < argc) {
            mv.tune = argv[argi++];
        } else if (!strcmp(opt, "-profile") && argi < argc) {
            xc.profile = argv[argi++];
        } else if (!strcmp(opt, "-level") && argi < argc) {
            xc.level = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-hightier")) {
            mv.high_tier = 1;
        } else if (!strcmp(opt, "-fps") && argi < argc) {
            if (sscanf(argv[argi], "%d/%d", &mv.fps_num, &mv.fps_den) != 2 ||
                mv.fps_num <= 0 || mv.fps_den <= 0) {
                fprintf(stderr, "Invalid fps '%s', expected num/den\n", argv[argi]);
                return 1;
            }
            argi++;
        } else if (!strcmp(opt, "-max-cll") && argi < argc) {
            xc.max_cll = argv[argi++];
        } else if (!strcmp(opt, "-master-display") && argi < argc) {
            xc.master_display = argv[argi++];
        } else if (!strcmp(opt, "-bitdepth") && argi < argc) {
            xc.bitdepth = atoi(argv[argi++]);
        } else if (!strcmp(opt, "-scenecut") && argi < argc) {
            mv.scenecut = atoi(argv[argi++]);
        } else {
            fprintf(stderr, "Unknown option: %s\n", opt);
            return 1;
        }
    }

    const char *left_file, *right_file, *out_file;

    if (single_input) {
        /* -i mode: expect 1 positional arg (output) */
        if (argc - argi != 1) {
            fprintf(stderr, "With -i, expected 1 positional arg: <output.hevc>\n");
            return 1;
        }
        left_file  = single_input;
        right_file = NULL;
        out_file   = argv[argi];
    } else {
        /* Two-file mode: expect 3 positional args */
        if (argc - argi != 3) {
            fprintf(stderr, "Expected 3 positional args: <left_eye> <right_eye> <output.hevc>\n");
            return 1;
        }
        left_file  = argv[argi];
        right_file = argv[argi + 1];
        out_file   = argv[argi + 2];
    }

    /* Init -> Transcode -> Cleanup */
    mvhevc_ctx *ctx = NULL;
    int rc;

    rc = mvhevc_init(&ctx, &xc, &mv, left_file, right_file, out_file);
    if (rc < 0) {
        fprintf(stderr, "mvhevc_init failed: %d\n", rc);
        mvhevc_fini(&ctx);
        return 1;
    }

    rc = mvhevc_xc(ctx);
    if (rc < 0) {
        fprintf(stderr, "mvhevc_xc failed: %d\n", rc);
        mvhevc_fini(&ctx);
        return 1;
    }

    fprintf(stderr, "Output: %s\n", out_file);
    mvhevc_fini(&ctx);
    return 0;
}
