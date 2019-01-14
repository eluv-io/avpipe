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

elv_channel_t *chanReq;
elv_channel_t *chanRep;

int InReaderX(int, char*, int);

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

    avpipe_msg_t *msg = (avpipe_msg_t *) calloc(1, sizeof(avpipe_msg_t));
    msg->mtype = avpipe_in_opener;
    msg->opaque = strdup(url);
    elv_channel_send(chanReq, msg);

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
    uint8_t buf2[64*1024];
    int fd = *((int *)(c->opaque));
    avpipe_msg_reply_t *reply;
    int n;

    elv_dbg("IN READ buf_size=%d fd=%d", buf_size, fd);

    int r = read(fd, buf2, buf_size);
    /* TODO: move this to go handlers
    if (r >= 0) {
        c->read_bytes += r;
        c->read_pos += r;
    }
    elv_dbg("IN READ read=%d pos=%"PRId64" total=%"PRId64, r, c->read_pos, c->read_bytes);
    */

#if 0
    avpipe_msg_t *msg = (avpipe_msg_t *) calloc(1, sizeof(avpipe_msg_t));
    msg->mtype = avpipe_in_reader;
    msg->opaque = opaque;
    msg->buf = buf;
    msg->buf_size = buf_size;
    elv_channel_send(chanReq, msg);

    reply = (avpipe_msg_reply_t *) elv_channel_receive(chanRep);
    n = reply->rc;
    elv_log("IN READ reply n=%d", n);
    free(reply);
#else
    n = InReaderX(0, (char *)buf, buf_size);
    elv_log("IN READ reply n=%d", n);
#endif

    /* TODO: read the return value properly */
    return n;
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

    avpipe_msg_t *msg = (avpipe_msg_t *) calloc(1, sizeof(avpipe_msg_t));
    msg->mtype = avpipe_in_seeker;
    msg->offset = offset;
    msg->whence = whence;
    elv_channel_send(chanReq, msg);

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

    elv_dbg("IN SEEK offset=%d whence=%d", offset, whence);

    return c->read_pos;
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

    avpipe_msg_t *msg = (avpipe_msg_t *) calloc(1, sizeof(avpipe_msg_t));
    msg->mtype = avpipe_out_opener;
    msg->opaque = strdup(segname);
    elv_channel_send(chanReq, msg);

    return 0;
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
    /* TODO move this to go 
    if (bwritten >= 0) {
        outctx->written_bytes += bwritten;
        outctx->write_pos += bwritten;
    }
    
    elv_dbg("OUT WRITE size=%d written=%d pos=%d total=%d", buf_size, bwritten, outctx->write_pos, outctx->written_bytes);
    */

    avpipe_msg_t *msg = (avpipe_msg_t *) calloc(1, sizeof(avpipe_msg_t));
    msg->mtype = avpipe_out_writer;
    msg->buf = buf;
    msg->buf_size = buf_size;
    elv_channel_send(chanReq, msg);
    
    return buf_size;
}

int64_t
out_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    elv_dbg("OUT SEEK offset=%d whence=%d", offset, whence);

    avpipe_msg_t *msg = (avpipe_msg_t *) calloc(1, sizeof(avpipe_msg_t));
    msg->mtype = avpipe_out_seeker;
    msg->offset = offset;
    msg->whence = whence;
    elv_channel_send(chanReq, msg);

    return 0;
}

int
out_closer(
    ioctx_t *outctx)
{
    avpipe_msg_t *msg = (avpipe_msg_t *) calloc(1, sizeof(avpipe_msg_t));
    msg->mtype = avpipe_out_closer;
    msg->buf = outctx->buf;
    elv_channel_send(chanReq, msg);

    return 0;
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
