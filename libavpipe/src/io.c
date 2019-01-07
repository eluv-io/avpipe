/*
 * io.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libavutil/log.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>

#include "elv_xc_test.h"
#include "elv_xc_utils.h"
#include "elv_log.h"

#include <stdio.h>
#include <fcntl.h>
#include <assert.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/uio.h>
#include <unistd.h>
#include <errno.h>


int
outbuf_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    buf_writer_t *w = (buf_writer_t *)opaque;
    elv_dbg("OUT READ buf_size=%d fd=%d", buf_size, w->fd);

    int bread = read(w->fd, buf, buf_size);
    if (bread >= 0) {
        w->bytes_read += bread;
        w->rpos += bread;
    }
    elv_dbg("OUT READ read=%d pos=%d total=%d", bread, w->rpos, w->bytes_read);

    return bread;
}

int
outbuf_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    buf_writer_t *w = (buf_writer_t *)opaque;

    int bwritten = write(w->fd, buf, buf_size);
    if (bwritten >= 0) {
        w->bytes_written += bwritten;
        w->wpos += bwritten;
    }
    elv_dbg("OUT WRITE fd=%d size=%d written=%d pos=%d total=%d", w->fd, buf_size, bwritten, w->wpos, w->bytes_written);
    return bwritten;
}

int64_t
outbuf_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    buf_writer_t *w = (buf_writer_t *)opaque;

    int rc = lseek(w->fd, offset, whence);
    whence = whence & 0xFFFF; /* Mask out AVSEEK_SIZE and AVSEEK_FORCE */
    switch (whence) {
    case SEEK_SET:
        w->rpos = offset; break;
    case SEEK_CUR:
        w->rpos += offset; break;
    case SEEK_END:
        w->rpos = -1;
        elv_dbg("IN SEEK - SEEK_END not yet implemented\n");
        break;
    default:
        elv_dbg("IN SEEK - weird seek\n");
    }

    elv_dbg("IN SEEK offset=%d whence=%d rc=%d", offset, whence, rc);
    return rc;
}

/*
 * Returns the AVIOContext as output argument 'pb'
 */
int
elv_io_open(
    struct AVFormatContext *format_ctx,
    AVIOContext **pb,
    const char *url,
    int flags,
    AVDictionary **options)
{
    int ret;

    int use_outbuf = 0;
    if (strstr(url, "chunk")) {
        use_outbuf = 1;
    }
    elv_dbg("OUT io_open url=%s use_outbuf=%d", url, use_outbuf);

    if (!use_outbuf) {
        ret = avio_open2(pb, url, flags, &format_ctx->interrupt_callback, options);
    } else {
        char segname[128];
        const char *segbase = "hunk-stream";
        char *endptr;
        int stream_index = 0;
        out_handler_t *out_handler = (out_handler_t *) format_ctx->avpipe_opaque;

        AVDictionaryEntry *stream_opt = av_dict_get(*options, "stream_index", 0, 0);
        buf_writer_t *w = (buf_writer_t *) calloc(1, sizeof(buf_writer_t));
        stream_index = (int) strtol(stream_opt->value, &endptr, 10);
        assert(stream_index == 0 || stream_index == 1);
        sprintf(segname, "./O/%s%s-%05d.m4s", segbase, stream_opt->value, ++out_handler[stream_index].chunk_idx);
        w->fd = open(segname, O_RDWR | O_CREAT | O_TRUNC, 0644);
        if (w->fd < 0) {
            elv_err("Failed to open segment file %s (%d)", segname, errno);
            return -1;
        }

        w->bufsz = 1 * 1024 * 1024;
        w->buf = (unsigned char *)malloc(w->bufsz); /* Must be malloc'd - will be realloc'd by avformat */

        AVIOContext *avioctx = avio_alloc_context(w->buf, w->bufsz, AVIO_FLAG_WRITE, (void *)w,
            outbuf_read_packet, outbuf_write_packet, outbuf_seek);

        avioctx->seekable = 0;
        avioctx->direct = 1;
        (*pb) = avioctx;

        out_handler[stream_index].last_buf_writer = w;
        w->out_handler = &out_handler[stream_index];

        ret = 0;
        elv_dbg("OUT io_open custom buf_writer=%p fd=%d\n", w, w->fd);
    }

    return ret;
}

void
elv_io_close(
    struct AVFormatContext *format_ctx,
    AVIOContext *pb)
{
    out_handler_t *out_handler = (out_handler_t *) format_ctx->avpipe_opaque;
    elv_dbg("OUT io_close opaque=%p\n", pb->opaque);

    if (pb->opaque == out_handler[0].last_buf_writer || pb->opaque == out_handler[1].last_buf_writer) {
        buf_writer_t *w = (buf_writer_t *)pb->opaque;
        elv_dbg("OUT io_close custom writer fd=%d\n", w->fd);
        (void)close(w->fd);
        free(w->buf);
        free(w);
    } else {
        avio_close(pb);
    }
    return;
}
