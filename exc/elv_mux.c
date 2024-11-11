#include <string.h>
#include <unistd.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <fcntl.h>

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"

/*
 * url points to filename/url of input file that should be muxed.
 * inctx hold the io context for url.
 */
static int
in_mux_opener(
    const char *url,
    ioctx_t *inctx)
{
    int fd = open(url, O_RDONLY);
    if (fd < 0) {
        elv_err("Failed to open mux input url=%s error=%d", url, errno);
        return -1;
    }

    elv_dbg("IN MUX OPEN fd=%"PRId64, fd);
    *((int *)(inctx->opaque)) = fd;
    return 0;
}

static int
in_mux_closer(
    ioctx_t *inctx)
{
    elv_dbg("IN MUX CLOSE\n");
    return 0;
}

unsigned long flip(unsigned long val) {
    unsigned long new = 0;
    new += (val & 0x000000FF) << 24;
    new += (val & 0xFF000000) >> 24;
    new += (val & 0x0000FF00) << 8;
    new += (val & 0x00FF0000) >> 8;

   return new;
}

#if 0
static int
read_header(
    int fd,
    char *mux_type)
{
    char buf[1024];
    int offset = 0;
    int len = 0;

    if (strcmp(mux_type, "mez-mux"))
        return 0;

    int r = read(fd, buf, 1024);
    if (r <= 0)
        return r;

    for (int i=0; i<1024-strlen("moov"); i++) {
        if (memcmp(&buf[i], "moov", 4) == 0) {
            offset = i;
            break;
        }
    }

    len = *((int *)&buf[offset-4]);
    int flipped_len = flip(len);
    elv_dbg("read_header offset=%d, len=%d, flip=%d", offset, len, flipped_len);
    lseek(fd, flipped_len+offset-4, SEEK_SET);
    return flipped_len+offset-4;
}
#endif

static int
in_mux_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *c = (ioctx_t *)opaque;
    int fd;
    int r = 0;

read_next_input:
    /* This means the input file is not opened yet, so open the input file */
    if (!c->opaque) {
        io_mux_ctx_t *in_mux_ctx = c->in_mux_ctx;
        int index = c->in_mux_index;
        char *filepath = NULL;

        /* index 0 means video */
        if (index == 0) {
            /* Reached end of videos */
            if (in_mux_ctx->video.index >= in_mux_ctx->video.n_parts)
                return AVERROR_EOF;
            filepath = in_mux_ctx->video.parts[in_mux_ctx->video.index];
            in_mux_ctx->video.index++;
        } else if (index <= in_mux_ctx->last_audio_index) {
            if (in_mux_ctx->audios[index-1].index >= in_mux_ctx->audios[index-1].n_parts)
                return AVERROR_EOF;
            filepath = in_mux_ctx->audios[index-1].parts[in_mux_ctx->audios[index-1].index];
            in_mux_ctx->audios[index-1].index++;
        } else if (index <= in_mux_ctx->last_audio_index+in_mux_ctx->last_caption_index) {
            if (in_mux_ctx->captions[index - in_mux_ctx->last_audio_index - 1].index >=
                in_mux_ctx->captions[index - in_mux_ctx->last_audio_index - 1].n_parts)
                return AVERROR_EOF;
            filepath = in_mux_ctx->captions[index - in_mux_ctx->last_audio_index - 1].parts[in_mux_ctx->captions[index - in_mux_ctx->last_audio_index - 1].index];
            in_mux_ctx->captions[index - in_mux_ctx->last_audio_index - 1].index++;
        } else {
            elv_err("in_mux_read_packet invalid mux index=%d", index);
            return -1;
        }

        c->opaque = (int *) calloc(1, sizeof(int));
        if (in_mux_opener(filepath, c) < 0) {
            elv_err("in_mux_read_packet failed to open file=%s", filepath);
            free(c->opaque);
            c->opaque = NULL;
            return -1;
        }
        fd = *((int64_t *)(c->opaque));

#if 0
        /* PENDING(RM) do we need to skip the header for multiple mez inputs?? */ 
        if (new_video)
            in_mux_ctx->video.header_size = read_header(fd, in_mux_ctx->mux_type);
        else if (new_audio)
            in_mux_ctx->audios[index-1].header_size = read_header(fd, in_mux_ctx->mux_type);
#endif

        elv_dbg("IN MUX READ opened new file filepath=%s, fd=%d", filepath, fd);
    }

    fd = *((int *)(c->opaque));

    r = read(fd, buf, buf_size);
    elv_dbg("IN MUX READ index=%d, buf=%p buf_size=%d fd=%d, r=%d",
        c->in_mux_index, buf, buf_size, fd, r);

    /* If it is EOF, read the next input file */
    if (r == 0) {
        elv_dbg("IN MUX READ closing file fd=%d", fd);
        close(fd);
        free(c->opaque);
        c->opaque = NULL;
        goto read_next_input;
    }

    if (r > 0) {
        c->read_bytes += r;
        c->read_pos += r;
    }
    elv_dbg("IN MUX READ index=%d, read=%d pos=%"PRId64" total=%"PRId64,
        c->in_mux_index, r, c->read_pos, c->read_bytes);

    return r > 0 ? r : -1;
}

static int
in_mux_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    elv_dbg("IN MUX WRITE");
    return 0;
}

static int64_t
in_mux_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *c = (ioctx_t *)opaque;
    int64_t rc = -1;
    elv_dbg("IN MUX SEEK index=%d, offset=%"PRId64" whence=%d rc=%"PRId64,
        c->in_mux_index, offset, whence, rc);
    return rc;
}

static int
out_mux_opener(
    const char *url,
    ioctx_t *outctx)
{
    int fd;

    /* If there is no url, return error. */
    if (!url) {
        elv_err("Failed to open mux output file, url is NULL");
        return -1;
    }

    fd = open(url, O_RDWR | O_CREAT | O_TRUNC, 0644);
    if (fd < 0) {
        elv_err("Failed to open mux segment file %s (%d)", url, errno);
        return -1;
    }

    outctx->opaque = (int *) malloc(sizeof(int));
    *((int *)(outctx->opaque)) = fd;

    outctx->bufsz = AVIO_OUT_BUF_SIZE;
    outctx->buf = (unsigned char *)malloc(outctx->bufsz); /* Must be malloc'd - will be realloc'd by avformat */
    elv_dbg("OUT MUX OPEN outctx=%p, path=%s, type=%d, fd=%d, seg_index=%d\n", outctx, url, outctx->type, fd, outctx->seg_index);
    return 0;
}

static int
out_mux_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int fd = *(int *)outctx->opaque;
    int bread;

    elv_dbg("OUT MUX READ buf_size=%d fd=%d", buf_size, fd);

    bread = read(fd, buf, buf_size);
    if (bread >= 0) {
        outctx->read_bytes += bread;
        outctx->read_pos += bread;
    }

    elv_dbg("OUT MUX READ read=%d pos=%d total=%"PRId64, bread, outctx->read_pos, outctx->read_bytes);

    return bread;
}

static int
out_mux_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int fd = *(int *)outctx->opaque;
    int bwritten;

    bwritten = write(fd, buf, buf_size);
    if (bwritten >= 0) {
        outctx->written_bytes += bwritten;
        outctx->write_pos += bwritten;
    }

    elv_dbg("OUT MUX WRITE fd=%d size=%d written=%d pos=%"PRId64" total=%"PRId64, fd, buf_size, bwritten, outctx->write_pos, outctx->written_bytes);
    return bwritten;
}

static int64_t
out_mux_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int fd = *(int *)outctx->opaque;

    int64_t rc = lseek(fd, offset, whence);
    whence = whence & 0xFFFF; /* Mask out AVSEEK_SIZE and AVSEEK_FORCE */
    switch (whence) {
    case SEEK_SET:
        outctx->read_pos = offset; break;
    case SEEK_CUR:
        outctx->read_pos += offset; break;
    case SEEK_END:
        outctx->read_pos = -1;
        elv_dbg("OUT MUX SEEK - SEEK_END not yet implemented\n");
        break;
    default:
        elv_err("OUT MUX SEEK - weird seek\n");
    }

    elv_dbg("OUT MUX SEEK offset=%"PRId64" whence=%d rc=%"PRId64, offset, whence, rc);
    return rc;
}

static int
out_mux_closer(
    ioctx_t *outctx)
{
    int fd = *((int *)(outctx->opaque));
    elv_dbg("OUT MUX CLOSE fd=%d\n", fd);
    close(fd);
    free(outctx->opaque);
    free(outctx->buf);
    return 0;
}

static int
out_mux_stat(
    void *opaque,
    int stream_index,
    avp_stat_t stat_type)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int64_t fd = *(int64_t *)outctx->opaque;

    if (outctx->type != avpipe_video_segment &&
        outctx->type != avpipe_audio_segment &&
        outctx->type != avpipe_mp4_stream &&
        outctx->type != avpipe_fmp4_stream &&
        outctx->type != avpipe_mp4_segment &&
        outctx->type != avpipe_video_fmp4_segment &&
        outctx->type != avpipe_audio_fmp4_segment)
        return 0;

    switch (stat_type) {
    case out_stat_bytes_written:
        elv_log("OUT MUX STAT stream_index=%d, fd=%d, write offset=%"PRId64,
            stream_index, fd, outctx->written_bytes);
        break;
#if 0
    /* PENDING(RM) set the hooks properly for muxing */
    case out_stat_decoding_start_pts:
        elv_log("OUT MUX STAT fd=%d, start PTS=%"PRId64, fd, outctx->decoding_start_pts);
        break;
    case out_stat_encoding_end_pts:
        elv_log("OUT MUX STAT fd=%d, end PTS=%"PRId64, fd, outctx->encoder_ctx->input_last_pts_sent_encode);
        break;
#endif
    default:
        break;
    }
    return 0;
}

int
do_mux(
    xcparams_t *params,
    char *out_filename
)
{
    io_mux_ctx_t in_mux_ctx;
    avpipe_io_handler_t *in_handlers;
    avpipe_io_handler_t *out_handlers;
    //char *url = "fsegment-%05d.mp4";
    char *url = out_filename;
    xctx_t *xctx;
    int rc = 0;

    in_handlers = (avpipe_io_handler_t *) calloc(1, sizeof(avpipe_io_handler_t));
    in_handlers->avpipe_opener = in_mux_opener;
    in_handlers->avpipe_closer = in_mux_closer;
    in_handlers->avpipe_reader = in_mux_read_packet;
    in_handlers->avpipe_writer = in_mux_write_packet;
    in_handlers->avpipe_seeker = in_mux_seek;

    out_handlers = (avpipe_io_handler_t *) calloc(1, sizeof(avpipe_io_handler_t));
    out_handlers->avpipe_opener = out_mux_opener;
    out_handlers->avpipe_closer = out_mux_closer;
    out_handlers->avpipe_reader = out_mux_read_packet;
    out_handlers->avpipe_writer = out_mux_write_packet;
    out_handlers->avpipe_seeker = out_mux_seek;
    out_handlers->avpipe_stater = out_mux_stat;

    memset(&in_mux_ctx, 0, sizeof(io_mux_ctx_t));

    rc = avpipe_init_muxer(&xctx, in_handlers, &in_mux_ctx, out_handlers, params);
    if (rc < 0) {
        printf("Error: avpipe init muxer failed, url=%s.\n", url);
        goto end_muxing;
    }

    rc = avpipe_mux(xctx);
    if (rc < 0) {
        printf("Error: avpipe muxing failed url=%s.\n", url);
        goto end_muxing;
    }

end_muxing:
    elv_dbg("Releasing all the muxing resources");
    avpipe_mux_fini(&xctx);
    return rc;
}
