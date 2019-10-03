/*
 * Test a/v transcoding pipeline
 *
 */

#include <sys/types.h>
#include <sys/stat.h>
#include <unistd.h>
#include <fcntl.h>
#include <libavutil/log.h>
#include <libavutil/pixdesc.h>
#include <errno.h>
#include <pthread.h>

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"

static int opened_inputs = 0;
static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;

int
in_opener(
    const char *url,
    ioctx_t *inctx)
{
    struct stat stb;
    int fd = open(url, O_RDONLY);
    if (fd < 0) {
        elv_err("Failed to open input url=%s error=%d", url, errno);
        return -1;
    }

    inctx->opaque = (int *) calloc(1, 2*sizeof(int));
    *((int *)(inctx->opaque)) = fd;
    if (fstat(fd, &stb) < 0) {
        free(inctx->opaque);
        return -1;
    }

    pthread_mutex_lock(&lock);
    opened_inputs++;
    *((int *)(inctx->opaque)+1) = opened_inputs;
    pthread_mutex_unlock(&lock);

    inctx->sz = stb.st_size;
    elv_dbg("IN OPEN fd=%d url=%s", fd, url);
    return 0;
}

int
in_closer(
    ioctx_t *inctx)
{
    int fd = *((int *)(inctx->opaque));
    elv_dbg("IN io_close custom writer fd=%d\n", fd);
    free(inctx->opaque);
    close(fd);
    return 0;
}

int
in_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *c = (ioctx_t *)opaque;
    int fd = *((int *)(c->opaque));
    elv_dbg("IN READ buf=%p buf_size=%d fd=%d", buf, buf_size, fd);

    int r = read(fd, buf, buf_size);
    if (r >= 0) {
        c->read_bytes += r;
        c->read_pos += r;
    }
    elv_dbg("IN READ read=%d pos=%"PRId64" total=%"PRId64, r, c->read_pos, c->read_bytes);

    return r > 0 ? r : -1;
}

int
in_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    elv_dbg("IN WRITE");
    return 0;
}

int64_t
in_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *c = (ioctx_t *)opaque;
    int fd = *((int *)(c->opaque));
    int64_t rc = lseek(fd, offset, whence);
    whence = whence & 0xFFFF; /* Mask out AVSEEK_SIZE and AVSEEK_FORCE */
    switch (whence) {
    case SEEK_SET:
        c->read_pos = offset; break;
    case SEEK_CUR:
        c->read_pos += offset; break;
    case SEEK_END:
        c->read_pos = c->sz - offset; break;
    default:
        elv_dbg("IN SEEK - weird seek\n");
    }

    elv_dbg("IN SEEK offset=%"PRId64" whence=%d rc=%"PRId64, offset, whence, rc);
    return rc;
}

int
out_opener(
    const char *url,
    ioctx_t *outctx)
{
    char segname[128];
    char dir[256];
    int fd;
    ioctx_t *inctx = outctx->inctx;
    int gfd = *((int *)(inctx->opaque)+1);
    struct stat st = {0};

    sprintf(dir, "./O/O%d", gfd);
    if (stat(dir, &st) == -1) {
        mkdir(dir, 0700);
    }

    /* If there is no url, just allocate the buffers. The data will be copied to the buffers */
    switch (outctx->type) {
    case avpipe_manifest:
        /* Manifest */
        sprintf(segname, "%s/%s", dir, "dash.mpd");
        break;

    case avpipe_master_m3u:
        /* HLS master mt38 */
        sprintf(segname, "%s/%s", dir, "master.m3u8");
        break;

    case avpipe_video_init_stream:
    case avpipe_audio_init_stream:
    case avpipe_video_m3u:
    case avpipe_audio_m3u:
    case avpipe_aes_128_key:
    case avpipe_mp4_stream:
    case avpipe_fmp4_stream:
        /* Init segments, or m3u files */
        sprintf(segname, "%s/%s", dir, url);
        break;

    case avpipe_video_segment:
    case avpipe_audio_segment:
        {
            const char *segbase = "chunk-stream";

            sprintf(segname, "./%s/%s%d-%05d.mp4",
                dir, segbase, outctx->stream_index, outctx->seg_index);
        }
        break;

    default:
        return -1;
    }

    fd = open(segname, O_RDWR | O_CREAT | O_TRUNC, 0644);
    if (fd < 0) {
        elv_err("Failed to open segment file %s (%d)", segname, errno);
        return -1;
    }

    outctx->opaque = (int *) malloc(sizeof(int));
    *((int *)(outctx->opaque)) = fd;

    outctx->bufsz = 1 * 1024 * 1024;
    outctx->buf = (unsigned char *)malloc(outctx->bufsz); /* Must be malloc'd - will be realloc'd by avformat */
    elv_dbg("OUT OPEN outctx=%p, path=%s, type=%d, fd=%d\n", outctx, segname, outctx->type, fd);
    return 0;
}

int
out_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int fd = *(int *)outctx->opaque;
    int bread;

    elv_dbg("OUT READ buf_size=%d fd=%d", buf_size, fd);

    bread = read(fd, buf, buf_size);
    if (bread >= 0) {
        outctx->read_bytes += bread;
        outctx->read_pos += bread;
    }

    elv_dbg("OUT READ read=%d pos=%d total=%d", bread, outctx->read_pos, outctx->read_bytes);

    return bread;
}

int
out_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int fd = *(int *)outctx->opaque;
    int bwritten;

    if (fd < 0) {
        /* If there is no space in outctx->buf, reallocate the buffer */
        if (outctx->bufsz-outctx->written_bytes < buf_size) {
            unsigned char *tmp = (unsigned char *) calloc(1, outctx->bufsz*2);
            memcpy(tmp, outctx->buf, outctx->written_bytes);
            outctx->bufsz = outctx->bufsz*2;
            free(outctx->buf);
            outctx->buf = tmp;
            elv_log("XXX growing the buffer to %d", outctx->bufsz);
        }

        elv_log("XXX2 MEMORY write sz=%d", buf_size);
        memcpy(outctx->buf+outctx->written_bytes, buf, buf_size);
        outctx->written_bytes += buf_size;
        outctx->write_pos += buf_size;
        bwritten = buf_size;
    }
    else {
        bwritten = write(fd, buf, buf_size);
        if (bwritten >= 0) {
            outctx->written_bytes += bwritten;
            outctx->write_pos += bwritten;
        }
    }

    elv_dbg("OUT WRITE fd=%d size=%d written=%d pos=%d total=%d", fd, buf_size, bwritten, outctx->write_pos, outctx->written_bytes);
    return bwritten;
}

int64_t
out_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int fd = *(int *)outctx->opaque;

    int rc = lseek(fd, offset, whence);
    whence = whence & 0xFFFF; /* Mask out AVSEEK_SIZE and AVSEEK_FORCE */
    switch (whence) {
    case SEEK_SET:
        outctx->read_pos = offset; break;
    case SEEK_CUR:
        outctx->read_pos += offset; break;
    case SEEK_END:
        outctx->read_pos = -1;
        elv_dbg("IN SEEK - SEEK_END not yet implemented\n");
        break;
    default:
        elv_err("OUT SEEK - weird seek\n");
    }

    elv_dbg("OUT SEEK offset=%d whence=%d rc=%d", offset, whence, rc);
    return rc;
}

int
out_closer(
    ioctx_t *outctx)
{
    int fd = *((int *)(outctx->opaque));
    elv_dbg("OUT CLOSE custom writer fd=%d\n", fd);
    close(fd);
    free(outctx->opaque);
    free(outctx->buf);
    return 0;
}

typedef struct tx_thread_params_t {
    int thread_number;
    char *filename;
    int repeats;
    int bypass_transcoding;
    txparams_t *txparams;
    avpipe_io_handler_t *in_handlers;
    avpipe_io_handler_t *out_handlers;
} tx_thread_params_t;

void *
tx_thread_func(
    void *thread_params)
{
    tx_thread_params_t *params = (tx_thread_params_t *) thread_params;
    txctx_t *txctx;
    int i;

    elv_log("TRANSCODER THREAD %d STARTS", params->thread_number);

    for (i=0; i<params->repeats; i++) {
        ioctx_t *inctx = (ioctx_t *)calloc(1, sizeof(ioctx_t));

        if (params->in_handlers->avpipe_opener(params->filename, inctx) < 0) {
            elv_err("THREAD %d, iteration %d failed to open avpipe output", params->thread_number, i+1);
            continue;
        }

        if (avpipe_init(&txctx, params->in_handlers, inctx, params->out_handlers, params->txparams, params->bypass_transcoding) < 0) {
            elv_err("THREAD %d, iteration %d, failed to initialize avpipe", params->thread_number, i+1);
            continue;
        }

        if (avpipe_tx(txctx, 0, params->bypass_transcoding, 1) < 0) {
            elv_err("THREAD %d, iteration %d error in transcoding", params->thread_number, i+1);
            continue;
        }

        /* Close input handler resources */
        params->in_handlers->avpipe_closer(inctx);

        elv_dbg("Releasing all the resources");
        avpipe_fini(&txctx);
    }

    elv_log("TRANSCODER THREAD %d ENDS", params->thread_number);

    return 0;
}

static tx_type_t
tx_type_from_string(
    char *tx_type_str
)
{
    if (!strcmp(tx_type_str, "all"))
        return tx_all;

    if (!strcmp(tx_type_str, "video"))
        return tx_video;

    if (!strcmp(tx_type_str, "audio"))
        return tx_audio;

    return tx_none;
}

static int
do_probe(
    char *filename,
    int seekable
)
{
    ioctx_t inctx;
    avpipe_io_handler_t in_handlers;
    txprobe_t *probe;
    int rc;

    in_handlers.avpipe_opener = in_opener;
    in_handlers.avpipe_closer = in_closer;
    in_handlers.avpipe_reader = in_read_packet;
    in_handlers.avpipe_writer = in_write_packet;
    in_handlers.avpipe_seeker = in_seek;

    if (in_handlers.avpipe_opener(filename, &inctx) < 0) {
        rc = -1;
        goto end_probe;
    }

    rc = avpipe_probe(&in_handlers, &inctx, seekable, &probe);
    if (rc < 0) {
        printf("Error: avpipe probe failed on file %s with no valid stream.\n", filename);
        goto end_probe;
    }

    for (int i=0; i<rc; i++) {
        printf("Stream[%d]\n"
                "\tcodec_type: %s\n"
                "\tcodec_id: %d\n"
                "\tcodec_name: %s\n"
                "\tduration_ts: %d\n"
                "\ttime_base: %d/%d\n"
                "\tnb_frames: %"PRId64"\n"
                "\tstart_time: %"PRId64"\n"
                "\tavg_frame_rate: %d/%d\n"
                "\tframe_rate: %d/%d\n"
                "\tticks_per_frame: %d\n"
                "\tbit_rate: %"PRId64"\n"
                "\twidth: %d\n"
                "\theight: %d\n"
                "\tpix_fmt: %s\n"
                "\thas_b_frames: %d\n"
                "\tfield_order: %d\n"
                "\tsample_aspect_ratio: %d/%d\n"
                "\tdisplay_aspect_ratio: %d/%d\n",
                i,
                av_get_media_type_string(probe->stream_info[i].codec_type),
                probe->stream_info[i].codec_id,
                probe->stream_info[i].codec_name,
                probe->stream_info[i].duration_ts,
                probe->stream_info[i].time_base.num,probe->stream_info[i].time_base.den,
                probe->stream_info[i].nb_frames,
                probe->stream_info[i].start_time,
                probe->stream_info[i].avg_frame_rate.num, probe->stream_info[i].avg_frame_rate.den,
                probe->stream_info[i].frame_rate.num, probe->stream_info[i].frame_rate.den,
                probe->stream_info[i].ticks_per_frame,
                probe->stream_info[i].bit_rate,
                probe->stream_info[i].width,
                probe->stream_info[i].height,
                av_get_pix_fmt_name(probe->stream_info[i].pix_fmt) != NULL ? av_get_pix_fmt_name(probe->stream_info[i].pix_fmt) : "-",
                probe->stream_info[i].has_b_frames,
                probe->stream_info[i].field_order,
                probe->stream_info[i].sample_aspect_ratio.num, probe->stream_info[i].sample_aspect_ratio.den,
                probe->stream_info[i].display_aspect_ratio.num, probe->stream_info[i].display_aspect_ratio.den
                );
    }
    printf("Container\n"
        "\tformat_name: %s\n"
        "\tduration: %.5f\n",
        probe->container_info.format_name,
        probe->container_info.duration);

end_probe:
    elv_dbg("Releasing probe resources");
    /* Close input handler resources */
    in_handlers.avpipe_closer(&inctx);
    return rc;
}

static void
usage(
    char *progname,
    char *bad_flag,
    int status
)
{
    printf(
        "Invalid parameter: %s\n\n"
        "Usage: %s <params>\n"
        "\t-audio-bitrate :     (optional) Default: -1\n"
        "\t-bypass :            (optional) bypass transcoding. Default is 0, must be 0 or 1\n"
        "\t-seekable :          (optional) seekable stream. Default is 0, must be 0 or 1\n"
        "\t-crf :               (optional) mutually exclusive with video-bitrate. Default: 23\n"
        "\t-crypt-iv :          (optional) 128-bit AES IV, as hex\n"
        "\t-crypt-key :         (optional) 128-bit AES key, as hex\n"
        "\t-crypt-kid :         (optional) 16-byte key ID, as hex\n"
        "\t-crypt-scheme :      (optional) encryption scheme. Default is \"none\", can be: \"aes-128\", \"cenc\", \"cbc1\", \"cens\", \"cbcs\"\n"
        "\t-crypt-url :         (optional) specify a key URL in the HLS manifest\n"
        "\t-d :                 (optional) decoder name. Default is \"h264\", can be: \"h264\", \"h264_cuvid\"\n"
        "\t-duration-ts :       (optional) Default: -1 (entire stream)\n"
        "\t-e :                 (optional) encoder name. Default is \"libx264\", can be: \"libx264\", \"h264_nvenc\", \"h264_videotoolbox\"\n"
        "\t-enc-height :        (optional) Default: -1 (use source height)\n"
        "\t-enc-width :         (optional) Default: -1 (use source width)\n"
        "\t-format :            (optional) package format. Default is \"dash\", can be: \"dash\", \"hls\", \"mp4\", or \"fmp4\"\n"
        "\t-sample-rate :       (optional) Default: -1\n"
        "\t-rc-buffer-size :    (optional)\n"
        "\t-rc-max-rate :       (optional)\n"
        "\t-start-pts :         (optional) starting PTS for output. Default is 0\n"
        "\t-start-frag-index :  (optional) start fragment index of first segment. Default is 0\n"
        "\t-start-segment :     (optional) start segment number >= 1, Default is 1\n"
        "\t-start-time-ts :     (optional) Default: 0\n"
        "\t-video-bitrate :     (optional) mutually exclusive with crf. Default: -1 (unused)\n"
        "\t-r :                 (optional) number of repeats. Default is 1 repeat, must be bigger than 1\n"
        "\t-t :                 (optional) transcoding threads. Default is 1 thread, must be bigger than 1\n"
        "\t-command :           (optional) directing command of etx, can be \"transcode\" or \"probe\" (default is transcode).\n"
        "\t-tx-type :           (optional) transcoding type. Default is \"all\", can be \"video\", \"audio\", or \"all\" \n"
        "\t-seg-duration-ts :   (mandatory) segment duration time base (positive integer).\n"
        "\t-seg-duration-fr :   (mandatory) segment duration frame (positive integer).\n"
        "\t-f :                 (mandatory) input filename for transcoding. Output goes to directory ./O\n",
        bad_flag, progname);
    exit(status);
}

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
    pthread_t *tids;
    tx_thread_params_t thread_params;
    avpipe_io_handler_t in_handlers;
    avpipe_io_handler_t out_handlers;
    struct stat st = {0};
    int repeats = 1;
    int n_threads = 1;
    char *filename = NULL;
    int bypass_transcoding = 0;
    int seekable = 0;
    int start_segment = -1;
    char *command = "transcode";
    int i;

    /* Parameters */
    txparams_t p = {
        .audio_bitrate = -1,                /* TODO - default? uses source bitrate? */
        .crf_str = "23",                    /* 1 best -> 23 standard middle -> 52 poor */
        .crypt_iv = NULL,
        .crypt_key = NULL,
        .crypt_key_url = NULL,
        .crypt_kid = NULL,
        .crypt_scheme = crypt_none,
        .dcodec = "",
        .duration_ts = -1,                  /* -1 means entire input stream, same units as input stream */
        .ecodec = "libx264",
        .enc_height = -1,                   /* -1 means use source height, other values 2160, 1080, 720 */
        .enc_width = -1,                    /* -1 means use source width, other values 3840, 1920, 1280 */
        .format = "dash",
        .rc_buffer_size = 4500000,          /* TODO - default? */
        .rc_max_rate = 6700000,             /* TODO - default? */
        .sample_rate = -1,                  /* Audio sampling rate 44100 */
        .seg_duration_fr = -1,              /* input argument, in frames-per-secoond units */
        .seg_duration_ts = -1,              /* input argument, same units as input stream PTS */
        .start_pts = 0,
        .start_segment_str = "1",           /* 1-based */
        .start_time_ts = 0,                 /* same units as input stream PTS */
        .start_fragment_index = 0,          /* Default is zero */
        .video_bitrate = -1,                /* not used if using CRF */
        .tx_type = tx_all,
        .seekable = 0
    };

    i = 1;
    while (i < argc) {
        if ((int) argv[i][0] != '-') {
            usage(argv[0], argv[i], EXIT_FAILURE);
        }
        switch ((int) argv[i][1]) {
        case 'a':
            if (!strcmp(argv[i], "-audio-bitrate")) {
                if (sscanf(argv[i+1], "%d", &p.audio_bitrate) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else {
                usage(argv[0], argv[i], EXIT_FAILURE);
            }
            break;
        case 'b':
            if (!strcmp(argv[i], "-bypass") || !strcmp(argv[i], "-b")) {
                if (sscanf(argv[i+1], "%d", &bypass_transcoding) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
                if (bypass_transcoding != 0 && bypass_transcoding != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else {
                usage(argv[0], argv[i], EXIT_FAILURE);
            }
            break;
        case 'c':
            if (!strcmp(argv[i], "-command")) {
                command = argv[i+1];
                if (!strcmp(command, "transcode") && !strcmp(command, "probe")) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-crf")) {
                p.crf_str = argv[i+1];
            } else if (strcmp(argv[i], "-crypt-iv") == 0) {
                p.crypt_iv = argv[i+1];
            } else if (strcmp(argv[i], "-crypt-key") == 0) {
                p.crypt_key = argv[i+1];
            } else if (strcmp(argv[i], "-crypt-kid") == 0) {
                p.crypt_kid = argv[i+1];
            } else if (strcmp(argv[i], "-crypt-scheme") == 0) {
                if (strcmp(argv[i+1], "aes-128") == 0) {
                    p.crypt_scheme = crypt_aes128;
                } else if (strcmp(argv[i+1], "cenc") == 0) {
                    p.crypt_scheme = crypt_cenc;
                } else if (strcmp(argv[i+1], "cbc1") == 0) {
                    p.crypt_scheme = crypt_cbc1;
                } else if (strcmp(argv[i+1], "cens") == 0) {
                    p.crypt_scheme = crypt_cens;
                } else if (strcmp(argv[i+1], "cbcs") == 0) {
                    p.crypt_scheme = crypt_cbcs;
                } else {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (strcmp(argv[i], "-crypt-url") == 0) {
                p.crypt_key_url = argv[i+1];
            } else {
                usage(argv[0], argv[i], EXIT_FAILURE);
            }
            break;
        case 'd':
            if (!strcmp(argv[i], "-duration-ts")) {
                if (sscanf(argv[i+1], "%d", &p.duration_ts) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (strlen(argv[i]) > 2) {
                usage(argv[0], argv[i], EXIT_FAILURE);
            } else {
                p.dcodec = argv[i+1];
            }
            break;
        case 'e':
            if (!strcmp(argv[i], "-enc-height")) {
                if (sscanf(argv[i+1], "%d", &p.enc_height) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-enc-width")) {
                if (sscanf(argv[i+1], "%d", &p.enc_width) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (strlen(argv[i]) > 2) {
                usage(argv[0], argv[i], EXIT_FAILURE);
            } else {
                p.ecodec = argv[i+1];
            }
            break;
        case 'f':
            if (!strcmp(argv[i], "-format")) {
                if (strcmp(argv[i+1], "dash") == 0) {
                    p.format = "dash";
                } else if (strcmp(argv[i+1], "hls") == 0) {
                    p.format = "hls";
                } else if (strcmp(argv[i+1], "mp4") == 0) {
                    p.format = "mp4";
                } else if (strcmp(argv[i+1], "fmp4") == 0) {
                    p.format = "fmp4";
                } else {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (strlen(argv[i]) > 2) {
                usage(argv[0], argv[i], EXIT_FAILURE);
            } else {
                filename = argv[i+1];   
            }
            break;
        case 'r':
            if (!strcmp(argv[i], "-rc-buffer-size")) {
                if (sscanf(argv[i+1], "%d", &p.rc_buffer_size) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-rc-max-rate")) {
                if (sscanf(argv[i+1], "%d", &p.rc_max_rate) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (strlen(argv[i]) > 2) {
                usage(argv[0], argv[i], EXIT_FAILURE);
            } else {
                if (sscanf(argv[i+1], "%d", &repeats) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
                if (repeats < 1) usage(argv[0], argv[i], EXIT_FAILURE);
            }
            break;
        case 's':
            if (!strcmp(argv[i], "-seekable")) {
                if (sscanf(argv[i+1], "%d", &seekable) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
                if (seekable != 0 && seekable != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-sample-rate")) {
                if (sscanf(argv[i+1], "%d", &p.sample_rate) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-seg-duration-fr")) {
                if (sscanf(argv[i+1], "%d", &p.seg_duration_fr) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-seg-duration-ts")) {
                if (sscanf(argv[i+1], "%d", &p.seg_duration_ts) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-start-pts")) {
                if (sscanf(argv[i+1], "%d", &p.start_pts) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-start-segment")) {
                p.start_segment_str = argv[i+1];
            } else if (!strcmp(argv[i], "-start-frag-index")) {
                if (sscanf(argv[i+1], "%d", &p.start_fragment_index) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else if (!strcmp(argv[i], "-start-time-ts")) {
                if (sscanf(argv[i+1], "%d", &p.start_time_ts) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else {
                usage(argv[0], argv[i], EXIT_FAILURE);
            }
            break;
        case 't':
            if (!strcmp(argv[i], "-tx-type")) {
                if (strcmp(argv[i+1], "all") && strcmp(argv[i+1], "video") && strcmp(argv[i+1], "audio")) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
                p.tx_type = tx_type_from_string(argv[i+1]);
            } else if (sscanf(argv[i+1], "%d", &n_threads) != 1) {
                usage(argv[0], argv[i], EXIT_FAILURE);
            }
            if ( n_threads < 1 ) usage(argv[0], argv[i], EXIT_FAILURE);
            break;
        case 'v':
            if (!strcmp(argv[i], "-video-bitrate")) {
                if (sscanf(argv[i+1], "%d", &p.video_bitrate) != 1) {
                    usage(argv[0], argv[i], EXIT_FAILURE);
                }
            } else {
                usage(argv[0], argv[i], EXIT_FAILURE);
            }
            break;
        default:
            usage(argv[0], argv[i], EXIT_FAILURE);
        }
        i += 2;
    }

    if (filename == NULL) {
        usage(argv[0], "-f", EXIT_FAILURE);
    }

    // Set AV libs log level and handle using elv_log
    av_log_set_level(AV_LOG_DEBUG);
    connect_ffmpeg_log();

    elv_logger_open(NULL, "etx", 10, 100*1024*1024, elv_log_file);
    elv_set_log_level(elv_log_debug);

    if (!strcmp(command, "probe")) {
        return do_probe(filename, seekable);
    }

    if (sscanf(p.start_segment_str, "%d", &start_segment) != 1) {
        usage(argv[0], "-start_segment", EXIT_FAILURE);
    }
    if (p.seg_duration_ts <= 0 || p.seg_duration_fr <= 0 || start_segment < 1) {
        usage(argv[0], "seg_duration_ts, seg_duration_fr, start_segment", EXIT_FAILURE);
    }

    /* Create O dir if doesn't exist */
    if (stat("./O", &st) == -1)
        mkdir("./O", 0700);

    elv_log("txparams:\n"
            "  audio_bitrate=%d\n"
            "  crf_str=%s\n"
            "  crypt_iv=%s\n"
            "  crypt_key=%s\n"
            "  crypt_key_url=%s\n"
            "  crypt_kid=%s\n"
            "  crypt_scheme=%d\n"
            "  dcodec=%s\n"
            "  duration_ts=%d\n"
            "  ecodec=%s\n"
            "  enc_height=%d\n"
            "  enc_width=%d\n"
            "  format=%s\n"
            "  rc_buffer_size=%d\n"
            "  rc_max_rate=%d\n"
            "  sample_rate=%d\n"
            "  seg_duration_fr=%d\n"
            "  seg_duration_ts=%d\n"
            "  start_pts=%d\n"
            "  start_segment_str=%s\n"
            "  start_time_ts=%d\n"
            "  video_bitrate=%d",
        p.audio_bitrate, p.crf_str, p.crypt_iv, p.crypt_key, p.crypt_key_url,
        p.crypt_kid, p.crypt_scheme, p.dcodec, p.duration_ts, p.ecodec,
        p.enc_height, p.enc_width, p.format, p.rc_buffer_size, p.rc_max_rate,
        p.sample_rate, p.seg_duration_fr, p.seg_duration_ts, p.start_pts,
        p.start_segment_str, p.start_time_ts, p.video_bitrate);

    in_handlers.avpipe_opener = in_opener;
    in_handlers.avpipe_closer = in_closer;
    in_handlers.avpipe_reader = in_read_packet;
    in_handlers.avpipe_writer = in_write_packet;
    in_handlers.avpipe_seeker = in_seek;

    out_handlers.avpipe_opener = out_opener;
    out_handlers.avpipe_closer = out_closer;
    out_handlers.avpipe_reader = out_read_packet;
    out_handlers.avpipe_writer = out_write_packet;
    out_handlers.avpipe_seeker = out_seek;

    thread_params.filename = strdup(filename);
    thread_params.repeats = repeats;
    thread_params.txparams = &p;
    thread_params.in_handlers = &in_handlers;
    thread_params.out_handlers = &out_handlers;
    thread_params.bypass_transcoding = bypass_transcoding;

    tids = (pthread_t *) calloc(1, n_threads*sizeof(pthread_t));

    for (i=0; i<n_threads; i++) {
        tx_thread_params_t *tp = (tx_thread_params_t *) malloc(sizeof(tx_thread_params_t));
        *tp = thread_params;
        tp->thread_number = i+1;
        pthread_create(&tids[i], NULL, tx_thread_func, tp);
    }

    for (i=0; i<n_threads; i++) {
        pthread_join(tids[i], NULL);
    }

    free(tids);

    return 0;
}
