/*
 * Test a/v transcoding pipeline
 *
 */

#include <sys/types.h>
#include <sys/stat.h>
#include <unistd.h>
#include <fcntl.h>
#include <libavutil/log.h>

#include "avpipe_xc.h"
#include "elv_log.h"
#include "elv_channel.h"
#include "goetx.h"


int InOpenerX(char*);
int InReaderX(char*, int);
int InSeekerX(int64_t offset, int whence);
int InCloserX();
int OutOpenerX(char *);
int OutWriterX(char *, int);
int OutSeekerX(int64_t offset, int whence);
int OutCloserX();

int
in_opener(
    const char *url,
    ioctx_t *inctx)
{
    struct stat stb;
    int fd = open(url, O_RDONLY);
    if (fd < 0) {
        return -1;
    }

    inctx->opaque = (int *) calloc(1, sizeof(int));
    *((int *)(inctx->opaque)) = fd;
    if (fstat(fd, &stb) < 0)
        return -1;

    inctx->sz = stb.st_size;

    int rc = InOpenerX((char *) url);
    elv_dbg("IN OPEN fd=%d", fd);

    return rc;
}

int
in_closer(
    ioctx_t *inctx)
{
    int fd = *((int *)(inctx->opaque));
    elv_dbg("IN io_close custom writer fd=%d\n", fd);
    free(inctx->opaque);
    close(fd);

    InCloserX();
    return 0;
}

int
in_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *c = (ioctx_t *)opaque;
    int r;

    elv_dbg("IN READ buf=%p, size=%d", buf, buf_size);

    char *buf2 = (char *) calloc(1, buf_size);
    int fd = *((int *)(c->opaque));
    elv_dbg("IN READ buf_size=%d fd=%d", buf_size, fd);
    int n = read(fd, buf2, buf_size);
    
    r = InReaderX((char *)buf, buf_size);
    if (r >= 0) {
        c->read_bytes += r;
        c->read_pos += r;
    }

    if ( r == n) {
        for (int i=0; i<r; i++) {
            if ( i< 10)
                elv_log("i=%d, buf=%d, buf2=%d", i, buf[i], buf2[i]);
            if ( buf[i] != buf2[i]) {
                elv_log("NOOOO! buffers don't match");
                break;
            }
        }
    }

    elv_dbg("IN READ read=%d pos=%"PRId64" total=%"PRId64, r, c->read_pos, c->read_bytes);
    free(buf2);

    return r;
}

int
in_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    elv_err("IN WRITE");
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

    int64_t rc = InSeekerX(offset, whence);
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

    elv_dbg("IN SEEK fd=%d, offset=%d, whence=%d, rc=%d", fd, offset, whence, rc);

    return rc;
}

int
out_opener(
    const char *url,
    ioctx_t *outctx)
{
    char segname[128];

    /* If there is no url, just allocate the buffers. The data will be copied to the buffers */
    switch (outctx->type) {
    case avpipe_manifest:
        /* Manifest */
        sprintf(segname, "./O/%s", "dash.mpd");
        break;

    case avpipe_init_stream:
        /* Init segments */
        sprintf(segname, "./O/%s", url);
        break;

    case avpipe_segment:
        {
            const char *segbase = "chunk-stream";

            sprintf(segname, "./%s/%s%d-%05d.mp4",
                "/O", segbase, outctx->stream_index, outctx->seg_index);
        }
        break;

    default:
        return -1;
    }

    outctx->bufsz = 1 * 1024 * 1024;
    outctx->buf = (unsigned char *)malloc(outctx->bufsz); /* Must be malloc'd - will be realloc'd by avformat */
    elv_dbg("OUT out_opener outctx=%p\n", outctx);

    int rc = OutOpenerX(segname);

    return rc;
}

int
out_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    elv_err("OUT READ called");

    return 0;
}

int
out_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int bwritten = OutWriterX(buf, buf_size);
    if (bwritten >= 0) {
        outctx->written_bytes += bwritten;
        outctx->write_pos += bwritten;
    }
    
    elv_dbg("OUT WRITE size=%d written=%d pos=%d total=%d", buf_size, bwritten, outctx->write_pos, outctx->written_bytes);
    
    return buf_size;
}

int64_t
out_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int rc = OutSeekerX(offset, whence);
    whence = whence & 0xFFFF; /* Mask out AVSEEK_SIZE and AVSEEK_FORCE */
    switch (whence) {
    case SEEK_SET:
        outctx->write_pos = offset; break;
    case SEEK_CUR:
        outctx->write_pos += offset; break;
    case SEEK_END:
        outctx->write_pos = outctx->sz - offset; break;
    default:
        elv_dbg("OUT SEEK - weird seek\n");
    }

    elv_dbg("OUT SEEK offset=%d whence=%d", offset, whence);

    return rc;
}

int
out_closer(
    ioctx_t *outctx)
{
    int rc = OutCloserX();

    return rc;
}

/*
 * Test basic decoding and encoding
 */
int
tx(
    txparams_t *params,
    char *filename)
{
    txctx_t *txctx;
    avpipe_io_handler_t in_handlers;
    avpipe_io_handler_t out_handlers;

    if (!filename || filename[0] == '\0' )
        return -1;

    elv_logger_open(NULL, "goetx", 10, 10*1024*1024, elv_log_file);
    elv_set_log_level(elv_log_debug);

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

    ioctx_t *inctx = (ioctx_t *)calloc(1, sizeof(ioctx_t));

    if (in_handlers.avpipe_opener(filename, inctx) < 0)
        return -1;

    if (avpipe_init(&txctx, &in_handlers, inctx, &out_handlers, params) < 0)
        return 1;

    if (avpipe_tx(txctx, 0) < 0) {
        elv_err("Error in transcoding");
        return -1;
    }

    elv_dbg("Releasing all the resources");
    avpipe_fini(&txctx);

    return 0;
}
