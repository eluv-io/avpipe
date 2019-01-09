/*
 * Test a/v transcoding pipeline
 *
 */

#include <sys/types.h>
#include <sys/stat.h>
#include <unistd.h>
#include <fcntl.h>
#include <libavutil/log.h>

#include "elv_xc.h"
#include "elv_log.h"

int
in_opener(
    char *filename,
    ioctx_t *inctx)
{
    struct stat stb;
    int fd = open(filename, O_RDONLY);
    if (fd < 0) {
        return -1;
    }

    inctx->opaque = (int *) calloc(1, sizeof(int));
    *((int *)(inctx->opaque)) = fd;
    if (fstat(fd, &stb) < 0)
        return -1;

    inctx->sz = stb.st_size;
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
    elv_dbg("IN READ buf_size=%d fd=%d", buf_size, fd);

    int r = read(fd, buf, buf_size);
    if (r >= 0) {
        c->read_bytes += r;
        c->read_pos += r;
    }
    elv_dbg("IN READ read=%d pos=%"PRId64" total=%"PRId64, r, c->read_pos, c->read_bytes);

    return r;
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
    char *filename,
    ioctx_t *outctx)
{
    const char *segbase = "hunk-stream";
    char segname[128];
    out_tracker_t *out_tracker = (out_tracker_t *) outctx->opaque;

    sprintf(segname, "./%s/%s%d-%05d.m4s",
        filename, segbase, outctx->stream_index, ++out_tracker[outctx->stream_index].chunk_idx);
    outctx->fd = open(segname, O_RDWR | O_CREAT | O_TRUNC, 0644);
    if (outctx->fd < 0) {
        elv_err("Failed to open segment file %s (%d)", segname, errno);
        return -1;
    }

    outctx->bufsz = 1 * 1024 * 1024;
    outctx->buf = (unsigned char *)malloc(outctx->bufsz); /* Must be malloc'd - will be realloc'd by avformat */
    elv_dbg("OUT out_opener outctx=%p fd=%d\n", outctx, outctx->fd);
    return 0;
}

int
out_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    elv_dbg("OUT READ buf_size=%d fd=%d", buf_size, outctx->fd);

    int bread = read(outctx->fd, buf, buf_size);
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

    int bwritten = write(outctx->fd, buf, buf_size);
    if (bwritten >= 0) {
        outctx->write_bytes += bwritten;
        outctx->write_pos += bwritten;
    }
    elv_dbg("OUT WRITE fd=%d size=%d written=%d pos=%d total=%d", outctx->fd, buf_size, bwritten, outctx->write_pos, outctx->write_bytes);
    return bwritten;
}

int64_t
out_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *outctx = (ioctx_t *)opaque;

    int rc = lseek(outctx->fd, offset, whence);
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
    avpipe_io_handler_t in_handlers;
    avpipe_io_handler_t out_handlers;

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

    in_handlers.avpipe_opener = in_opener;
    in_handlers.avpipe_reader = in_read_packet;
    in_handlers.avpipe_writer = in_write_packet;
    in_handlers.avpipe_seeker = in_seek;

    out_handlers.avpipe_opener = out_opener;
    out_handlers.avpipe_reader = out_read_packet;
    out_handlers.avpipe_writer = out_write_packet;
    out_handlers.avpipe_seeker = out_seek;

    ioctx_t *inctx = (ioctx_t *)calloc(1, sizeof(ioctx_t));

    if (in_handlers.avpipe_opener(argv[1], inctx) < 0)
        return -1;

    if (tx_init(&txctx, &in_handlers, inctx, &out_handlers, argv[2], &p) < 0)
        return 1;

    if (tx(txctx, 0) < 0) {
        elv_err("Error in transcoding");
        return -1;
    }

    elv_dbg("Releasing all the resources");
    tx_fini(&txctx);

    return 0;
}
