/*
 * Test a/v transcoding pipeline
 *
 */

#include <sys/types.h>
#include <sys/stat.h>
#include <unistd.h>
#include <fcntl.h>
#include <libavutil/log.h>
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
    int rc = lseek(fd, offset, whence);
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

    elv_dbg("IN SEEK offset=%d whence=%d rc=%d", offset, whence, rc);
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

        if (avpipe_tx(txctx, 0, params->bypass_transcoding) < 0) {
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

static void
usage(
    char *progname
)
{
    printf("Usage: %s <params>\n"
            "\t-bypass :            (optional) bypass transcoding. Default is 0, must be 0 or 1\n"
            "\t-r :                 (optional) number of repeats. Default is 1 repeat, must be bigger than 1\n"
            "\t-t :                 (optional) transcoding threads. Default is 1 thread, must be bigger than 1\n"
            "\t-e :                 (optional) encoder name. Default is \"libx264\", can be: \"libx264\", \"h264_nvenc\", \"h264_videotoolbox\"\n"
            "\t-d :                 (optional) decoder name. Default is \"h264\", can be: \"h264\", \"h264_cuvid\"\n"
            "\t-c :                 (optional) encryption scheme. Default is \"none\", can be: \"aes-128\"\n"
            "\t-k :                 (optional) 128-bit AES key, as hex\n"
            "\t-i :                 (optional) 128-bit AES IV, as hex\n"
            "\t-u :                 (optional) specify a key URL in the manifest\n"
            "\t-start-pts :         (optional) starting PTS for output. Default is 0\n"
            "\t-start-segment :     (optional) start segment number >= 1, Default is 1\n"
            "\t-seg-duration-ts :   (mandatory) segment duration time base (positive integer).\n"
            "\t-seg-duration-fr :   (mandatory) segment duration frame (positive integer).\n"
            "\t-timescale :         (mandatory) timescale based on time base (positive integer).\n"
            "\t-f :                 (mandatory) input filename for transcoding. Output goes to directory ./O\n", progname);
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
    int pts = 0;
    char *filename = NULL;
    int bypass_transcoding = 0;         // bypass transcoding
    int seg_duration_ts = 0;
    int seg_duration_fr = 0;
    int timescale = 0;
    char seg_duration_str[10];
    int start_segment = 1;
    char start_segment_str[10];
    int i;

    /* Parameters */
    txparams_t p = {
        .format = "hls",
        .video_bitrate = 2560000,           /* not used if using CRF */
        .audio_bitrate = 64000,
        .sample_rate = 44100,               /* Audio sampling rate */
        .crf_str = "23",                    /* 1 best -> 23 standard middle -> 52 poor */
        .start_time_ts = 0,                 /* same units as input stream PTS */
        .start_pts = 0,
        .duration_ts = -1,                  /* -1 means entire input stream, same units as input stream PT */
        .start_segment_str = "1",           /* 1-based */
        .seg_duration_ts = 0,               /* input argument, same units as input stream PTS */
        .seg_duration_fr = 30,              /* input argument, in frames-per-secoond units */
        .seg_duration_secs_str = "2.002",   /* deprecated, not used */
        .ecodec = "libx264",
        .dcodec = "",
        .enc_height = 2160,                 /* -1 means use source height, other values 2160, 1080, 720 */
        .enc_width = 1840,                  /* -1 means use source width, other values 3840, 1920, 1280 */
        .crypt_scheme = crypt_none,
        .crypt_key = NULL,
        .crypt_key_url = NULL,
        .crypt_iv = NULL
    };

    i = 1;
    while (i < argc) {
        switch ((int) argv[i][0]) {
        case '-':
            switch ((int) argv[i][1]) {
            case 'r':   /* repeats */
                if (sscanf(argv[i+1], "%d", &repeats) != 1) {
                    usage(argv[0]);
                    return 1;
                }

                if ( repeats < 1 ) {
                    usage(argv[0]);
                    return 1;
                }
                break;

            case 'e':
                p.ecodec = argv[i+1];
                break;

            case 'd':
                p.dcodec = argv[i+1];
                break;

            case 'f':   /* filename */
                filename = argv[i+1];
                break;

            case 't':   /* thread numbers */
                if (!strcmp(argv[i], "-timescale")) {
                    if (sscanf(argv[i+1], "%d", &timescale) != 1) {
                        usage(argv[0]);
                        return 1;
                    }
                    break;
                }

                if (sscanf(argv[i+1], "%d", &n_threads) != 1) {
                    usage(argv[0]);
                    return 1;
                }

                if ( n_threads < 1 ) {
                    usage(argv[0]);
                    return 1;
                }
                break;

            case 'b':
                if (!strcmp(argv[i], "-bypass") || !strcmp(argv[i], "-b")) {
                    if (sscanf(argv[i+1], "%d", &bypass_transcoding) != 1) {
                        usage(argv[0]);
                        return 1;
                    }

                    if (bypass_transcoding != 0 && bypass_transcoding != 1) {
                        usage(argv[0]);
                        return 1;
                    }
                }
                else {
                    usage(argv[0]);
                    return 1;
                }
                break;

            case 'c':
                if (strcmp(argv[i+1], "aes-128") == 0)
                    p.crypt_scheme = crypt_aes128;
                break;

            case 'k':
                p.crypt_key = argv[i+1];
                break;

            case 'i':
                p.crypt_iv = argv[i+1];
                break;

            case 'u':
                p.crypt_key_url = argv[i+1];
                break;

            case 's':
                if (!strcmp(argv[i], "-seg-duration-ts")) {
                    if (sscanf(argv[i+1], "%d", &seg_duration_ts) != 1) {
                        usage(argv[0]);
                        return 1;
                    }

                    if (seg_duration_ts <= 0) {
                        usage(argv[0]);
                        return 1;
                    }
                    break;
                }


                if (!strcmp(argv[i], "-seg-duration-fr")) {
                    if (sscanf(argv[i+1], "%d", &seg_duration_fr) != 1) {
                        usage(argv[0]);
                        return 1;
                    }

                    if (seg_duration_fr <= 0) {
                        usage(argv[0]);
                        return 1;
                    }
                    break;
                }

                if (!strcmp(argv[i], "-start-pts")) {
                    if (sscanf(argv[i+1], "%d", &pts) != 1) {
                        usage(argv[0]);
                        return 1;
                    }
                    break;
                }

                if (!strcmp(argv[i], "-start-segment")) {
                    if (sscanf(argv[i+1], "%d", &start_segment) != 1) {
                        usage(argv[0]);
                        return 1;
                    }
                    break;
                }

                break;

            default:
                usage(argv[0]);
                return -1;
            }
            i += 2;
            break;

        default:
            usage(argv[0]);
            return -1;
        }
    }

    if (filename == NULL) {
        usage(argv[0]);
        return -1;
    }

    if (seg_duration_ts <= 0 || seg_duration_fr <= 0 || timescale <= 0 || start_segment < 1) {
        usage(argv[0]);
        return -1;
    }

    sprintf(seg_duration_str, "%.4f", ((float)seg_duration_ts)/timescale);
    sprintf(start_segment_str, "%d", start_segment);

    p.start_pts = pts;
    p.seg_duration_secs_str = seg_duration_str;
    p.seg_duration_ts = seg_duration_ts;
    p.seg_duration_fr = seg_duration_fr;
    p.start_segment_str = start_segment_str;

    /* Create O dir if doesn't exist */
    if (stat("./O", &st) == -1)
        mkdir("./O", 0700);

    // Set AV libs log level and handle using elv_log
    av_log_set_level(AV_LOG_DEBUG);
    connect_ffmpeg_log();

    elv_logger_open(NULL, "etx", 10, 100*1024*1024, elv_log_file);
    elv_set_log_level(elv_log_debug);

    elv_log("seg_duration_str=%s, seg_duration_ts=%d, seg_duration_fr=%d, start_pts=%d, start_segment=%s",
        seg_duration_str, p.seg_duration_ts, p.seg_duration_fr, p.start_pts, p.start_segment_str);

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
        tx_thread_params_t *p = (tx_thread_params_t *) malloc(sizeof(tx_thread_params_t));
        *p = thread_params;
        p->thread_number = i+1;
        pthread_create(&tids[i], NULL, tx_thread_func, p);
    }

    for (i=0; i<n_threads; i++) {
        pthread_join(tids[i], NULL);
    }

    free(tids);

    return 0;
}
