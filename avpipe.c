/*
 * avpipe.c
 *
 * Implements generic C-GO handlers needed by avpipe GO.
 *
 */

#include <sys/types.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <unistd.h>
#include <fcntl.h>
#include <pthread.h>
#include <libavutil/log.h>
#include <libavutil/pixdesc.h>
#include <errno.h>
#include <pthread.h>
#include <srt.h>

#include "avpipe_utils.h"
#include "avpipe_xc.h"
#include "elv_log.h"
#include "elv_channel.h"
#include "avpipe.h"
#include "url_parser.h"
#include "elv_sock.h"

extern const char *
av_get_pix_fmt_name(
    enum AVPixelFormat pix_fmt);

extern const char *
avcodec_profile_name(
    enum AVCodecID codec_id,
    int profile);

extern void *
udp_thread_func(
    void *thread_params);

typedef struct udp_thread_params_t {
    int             fd;             /* Socket fd to read UDP datagrams */
    elv_channel_t   *udp_channel;   /* udp channel to keep incomming UDP packets */
    socklen_t       salen;
    ioctx_t         *inctx;
} udp_thread_params_t;

static int
out_stat(
    void *opaque,
    int stream_index,
    avp_stat_t stat_type);

int64_t AVPipeOpenInput(char *, int64_t *);
int64_t AVPipeOpenMuxInput(char *, char *, int64_t *);
int     AVPipeReadInput(int64_t, uint8_t *, int);
int64_t AVPipeSeekInput(int64_t, int64_t, int);
int     AVPipeCloseInput(int64_t);
int     AVPipeStatInput(int64_t, int, avp_stat_t, void *);
int64_t AVPipeOpenOutput(int64_t, int, int, int64_t, int);
int64_t AVPipeOpenMuxOutput(char *, int);
int     AVPipeWriteOutput(int64_t, int64_t, uint8_t *, int);
int     AVPipeWriteMuxOutput(int64_t, uint8_t *, int);
int64_t AVPipeSeekOutput(int64_t, int64_t, int64_t, int);
int64_t AVPipeSeekMuxOutput(int64_t, int64_t, int);
int     AVPipeCloseOutput(int64_t, int64_t);
int     AVPipeCloseMuxOutput(int64_t);
int     AVPipeStatOutput(int64_t, int64_t, int, avpipe_buftype_t, avp_stat_t, void *);
int     AVPipeStatMuxOutput(int64_t, int, avp_stat_t, void *);
int32_t GenerateAndRegisterHandle();
int     AssociateCThreadWithHandle(int32_t);
int     CLog(char *);
int     CDebug(char *);
int     CInfo(char *);
int     CWarn(char *);
int     CError(char *);

#define MIN_VALID_FD      (-4)

#define MAX_TX  128    /* Maximum transcodings per system */

typedef struct xc_entry_t {
    int32_t         handle;
    xctx_t          *xctx;
} xc_entry_t;

xc_entry_t          *xc_table[MAX_TX];
static pthread_mutex_t tx_mutex = PTHREAD_MUTEX_INITIALIZER;

static int
in_stat(
    void *opaque,
    int stream_index,
    avp_stat_t stat_type);

static int
in_opener(
    const char *url,
    ioctx_t *inctx)
{
    int64_t size;
    xcparams_t *xcparams = (inctx != NULL) ? inctx->params : NULL;

#ifdef CHECK_C_READ
    struct stat stb;
    int fd = open(url, O_RDONLY);
    if (fd < 0) {
        return -1;
    }

    /* Allocate space for both the AVPipeHandler and fd */
    inctx->opaque = (int *) calloc(1, sizeof(int)+sizeof(int64_t));
    *((int *)(((int64_t)inctx->opaque)+1)) = fd;
    if (fstat(fd, &stb) < 0)
        return -1;

    inctx->sz = stb.st_size;
    elv_dbg("IN OPEN fd=%d", fd);
#else
    inctx->opaque = (void *) calloc(1, sizeof(int64_t));
#endif

    if (url != NULL)
        inctx->url = strdup(url);
    else
        /* Default file input would be assumed to be mp4 */
        inctx->url = "bogus.mp4";

    int64_t fd = AVPipeOpenInput((char *) url, &size);
    if (fd <= 0 )
        return -1;

    if (size > 0)
        inctx->sz = size;
    if (xcparams && xcparams->debug_frame_level)
        elv_dbg("IN OPEN fd=%"PRId64", size=%"PRId64, fd, size);

    *((int64_t *)(inctx->opaque)) = fd;
    return 0;
}

static int
in_closer(
    ioctx_t *inctx)
{
#ifdef CHECK_C_READ
    int fd = *((int *)(((int64_t *)inctx->opaque)+1));
    elv_dbg("IN io_close custom reader fd=%d", fd);
    free(inctx->opaque);
    close(fd);
#endif

    if (!inctx || !inctx->opaque)
        return 0;

    int64_t h = *((int64_t *)(inctx->opaque));
    xcparams_t *xcparams = (inctx != NULL) ? inctx->params : NULL;
    if (xcparams && xcparams->debug_frame_level)
        elv_dbg("IN CLOSER h=%d", h);
    AVPipeCloseInput(h);
    return 0;
}

static int
in_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *inctx = (ioctx_t *)opaque;
    xcparams_t *xcparams = (inctx != NULL) ? inctx->params : NULL;
    int r;
    int64_t fd;

    if (xcparams && xcparams->debug_frame_level)
        elv_dbg("IN READ buf=%p, size=%d", buf, buf_size);

#ifdef CHECK_C_READ
    char *buf2 = (char *) calloc(1, buf_size);
    int fd2 = *((int *)(inctx->opaque+1));
    elv_dbg("IN READ buf_size=%d fd=%d", buf_size, fd);
    int n = read(fd2, buf2, buf_size);
#endif

    fd = *((int64_t *)(inctx->opaque));
    r = AVPipeReadInput(fd, buf, buf_size);
    if (r > 0) {
        inctx->read_bytes += r;
        inctx->read_pos += r;

        if (inctx->read_bytes - inctx->read_reported > BYTES_READ_REPORT) {
            /* Pass stream_index 0 (stream_index has no meaning for in_stat_bytes_read) */
            in_stat(opaque, 0, in_stat_bytes_read);
            inctx->read_reported = inctx->read_bytes;
        }
    }

#ifdef CHECK_C_READ
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
    free(buf2);
#endif

    if (xcparams && xcparams->debug_frame_level)
        elv_dbg("IN READ read=%d pos=%"PRId64" total=%"PRId64", checksum=%u",
            r, inctx->read_pos, inctx->read_bytes, r > 0 ? checksum(buf, r) : 0);
    return r > 0 ? r : -1;
}

static int
in_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    elv_err("IN WRITE");
    return 0;
}

static int64_t
in_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    int64_t fd;
    ioctx_t *inctx = (ioctx_t *)opaque;
    xcparams_t *xcparams = (inctx != NULL) ? inctx->params : NULL;
    int64_t rc;

    fd = *((int64_t *)(inctx->opaque));
    rc = AVPipeSeekInput(fd, offset, whence);
    if (rc < 0)
        return rc;

    whence = whence & 0xFFFF; /* Mask out AVSEEK_SIZE and AVSEEK_FORCE */
    switch (whence) {
    case SEEK_SET:
        inctx->read_pos = offset; break;
    case SEEK_CUR:
        inctx->read_pos += offset; break;
    case SEEK_END:
        inctx->read_pos = inctx->sz - offset; break;
    default:
        elv_dbg("IN SEEK - weird seek\n");
    }

    if (xcparams && xcparams->debug_frame_level)
        elv_dbg("IN SEEK offset=%"PRId64", whence=%d, rc=%"PRId64, offset, whence, rc);

    return rc;
}

static int
in_stat(
    void *opaque,
    int stream_index,
    avp_stat_t stat_type)
{
    int64_t fd;
    ioctx_t *c = (ioctx_t *)opaque;
    int64_t rc;

    if (!c || !c->opaque) {
        return 0;
    }

    fd = *((int64_t *)(c->opaque));

    switch (stat_type) {
    case in_stat_bytes_read:
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->read_bytes);
        break;

    case in_stat_decoding_audio_start_pts:
    case in_stat_decoding_video_start_pts:
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->decoding_start_pts);
        break;

    case in_stat_audio_frame_read:
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->audio_frames_read);
        break;

    case in_stat_video_frame_read:
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->video_frames_read);
        break;

    case in_stat_first_keyframe_pts:
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->first_key_frame_pts);
        break;

    case in_stat_data_scte35:
        rc = AVPipeStatInput(fd, stream_index, stat_type, c->data);
        break;

    default:
        rc = -1;
    }
    return rc;
}

static int
udp_in_opener(
    const char *url,
    ioctx_t *inctx)
{
    int                 rc;
    int                 sockfd;
    const int           on = 1;
    socklen_t           salen;
    int64_t             size;
    struct sockaddr     *sa;
    udp_thread_params_t *params;
    url_parser_t        url_parser;

    rc = parse_url((char *)url, &url_parser);
    if (rc) {
        elv_err("Failed to parse input url=%s", url);
        inctx->opaque = NULL;
        return -1;
    }

    sockfd = udp_socket(url_parser.host, url_parser.port, &sa, &salen);
    if (sockfd < 0) {
        elv_err("Failed to open input udp url=%s, error=%d", url, errno);
        inctx->opaque = NULL;
        return -1;
    }

    setsockopt(sockfd, SOL_SOCKET, SO_REUSEADDR, &on, sizeof(on));
    if ((rc = bind(sockfd, sa, salen)) < 0) {
        /* Can not bind, fail and exit */
        elv_err("Failed to bind UDP socket, rc=%d, url=%s, errno=%d", rc, url, errno);
        return -1;
    }

    struct timeval tv;
    tv.tv_sec = UDP_PIPE_TIMEOUT;
    tv.tv_usec = 0;
    if ((rc = setsockopt(sockfd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv))) < 0) {
        elv_err("Failed to set UDP socket timeout, rc=%d, url=%s, errno=%d", rc, url, errno);
        return -1;
    }

    size_t bufsz = UDP_PIPE_BUFSIZE;
    if (setsockopt(sockfd, SOL_SOCKET, SO_RCVBUF, (const void *)&bufsz, (socklen_t)sizeof(bufsz)) == -1) {
        elv_warn("Failed to set UDP socket buf size to=%"PRId64", url=%s, errno=%d", bufsz, url, errno);
    }

    if (set_sock_nonblocking(sockfd) < 0) {
        elv_err("Failed to make UDP socket nonblocking, errno=%d", errno);
        return -1;
    }


    elv_channel_init(&inctx->udp_channel, MAX_UDP_CHANNEL, NULL);
    inctx->opaque = (int *) calloc(1, sizeof(int)+sizeof(int64_t));
    *((int *)((int64_t *)inctx->opaque+1)) = sockfd;

    int64_t fd = AVPipeOpenInput((char *) url, &size);
    if (fd <= 0 )
        return -1;

    if (size > 0)
        inctx->sz = size;

    *((int64_t *)(inctx->opaque)) = fd;
    inctx->url = strdup(url);

    /* Start a thread to read into UDP channel */
    params = (udp_thread_params_t *) calloc(1, sizeof(udp_thread_params_t));
    params->fd = sockfd;
    params->salen = salen;
    params->udp_channel = inctx->udp_channel;
    params->inctx = inctx;

    // TODO(Nate): How to get the handle all the way into here??
    pthread_create(&inctx->utid, NULL, udp_thread_func, params);
    elv_dbg("IN OPEN UDP fd=%d, sockfd=%d, url=%s, tid=%"PRId64, fd, sockfd, url, inctx->utid);
    return 0;
}

static int
udp_in_closer(
    ioctx_t *inctx)
{
    if (!inctx->opaque)
        return 0;

    int fd = *((int64_t *)inctx->opaque);
    int sockfd = *((int *)((int64_t *)inctx->opaque+1));
    elv_dbg("IN CLOSE UDP fd=%d, sockfd=%d, url=%s\n", fd, sockfd, inctx->url ? inctx->url : "bogus.mp4");
    free(inctx->opaque);
    close(sockfd);
    return 0;
}

static int
udp_in_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *c = (ioctx_t *)opaque;
    xcparams_t *xcparams = c->params;
    int debug_frame_level = (xcparams != NULL) ? xcparams->debug_frame_level : 0;
    int r = 0;

    if (c->udp_channel) {
        udp_packet_t *udp_packet;
        int rc;

        if (c->closed)
            return -1;

        if (c->cur_packet) {
            r = buf_size > (c->cur_packet->len - c->cur_pread) ? (c->cur_packet->len - c->cur_pread) : buf_size;
            memcpy(buf, &c->cur_packet->buf[c->cur_pread], r);
            c->cur_pread += r;
            if (c->cur_pread == c->cur_packet->len) {
                free(c->cur_packet);
                c->cur_packet = NULL;
                c->cur_pread = 0;
            }
            c->read_bytes += r;
            c->read_pos += r;
            //elv_dbg("IN READ UDP partial read=%d pos=%"PRId64" total=%"PRId64, r, c->read_pos, c->read_bytes);
            return r;
        }

        /*
         * If there is no source then avpipe_init() would block forever when preparing decoder.
         * In this situation, cancelling a transcoding would become impossible since xc_init() has not completed yet.
         * In order to avoid this situation, there will be a UDP_PIPE_TIMEOUT sec timeout when reading from channel.
         */
read_channel_again:
        if (c->closed)
            return -1;

        rc = elv_channel_timed_receive(c->udp_channel, UDP_PIPE_TIMEOUT*1000000, (void **)&udp_packet);
        if (rc == ETIMEDOUT) {
            if (c->is_udp_started) {
                elv_log("TIMEDOUT in UDP rcv channel, url=%s", c->url);
                return AVERROR(ETIMEDOUT);
            }
            goto read_channel_again;
        }

        if (rc == EPIPE || elv_channel_is_closed(c->udp_channel)) {
            elv_dbg("IN READ UDP channel closed, url=%s", c->url);
            return -1;
        }

        if (!c->is_udp_started) {
            elv_log("READ first UDP packet url=%d", c->url);
            c->is_udp_started = 1;
        }

        r = buf_size > udp_packet->len ? udp_packet->len : buf_size;
        c->read_bytes += r;
        c->read_pos += r;
        memcpy(buf, udp_packet->buf, r);
        if (r < udp_packet->len) {
            c->cur_packet = udp_packet;
            c->cur_pread = r;
        } else {
            //elv_log("UDP FREE %d", udp_packet->pkt_num);
            free(udp_packet);
        }
        if (debug_frame_level)
            elv_dbg("IN READ UDP read=%d, pos=%"PRId64", total=%"PRId64", url=%s", r, c->read_pos, c->read_bytes, c->url);
    }

    return r;
}

static int
udp_in_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *inctx = (ioctx_t *)opaque;
    xcparams_t *xcparams = inctx->params;
    int debug_frame_level = (xcparams != NULL) ? xcparams->debug_frame_level : 0;
    if (debug_frame_level)
        elv_dbg("IN WRITE UDP, url=%s", inctx->url);
    return 0;
}

static int64_t
udp_in_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *c = (ioctx_t *)opaque;
    xcparams_t *xcparams = c->params;
    int debug_frame_level = (xcparams != NULL) ? xcparams->debug_frame_level : 0;
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
        if (debug_frame_level)
            elv_dbg("IN SEEK UDP - weird seek, url=%s", c->url);
    }

    if (debug_frame_level)
        elv_dbg("IN SEEK UDP offset=%"PRId64" whence=%d rc=%"PRId64", url=%s", offset, whence, rc, c->url);
    return rc;
}

static int
udp_in_stat(
    void *opaque,
    int stream_index,
    avp_stat_t stat_type)
{
    int64_t fd;
    ioctx_t *c = (ioctx_t *)opaque;
    int64_t rc;

    if (!c || !c->opaque)
        return 0;

    xcparams_t *xcparams = c->params;
    int debug_frame_level = (xcparams != NULL) ? xcparams->debug_frame_level : 0;
    fd = *((int64_t *)(c->opaque));
    switch (stat_type) {
    case in_stat_bytes_read:
        if (debug_frame_level)
            elv_dbg("IN STAT UDP fd=%d, read offset=%"PRId64", url=%s", fd, c->read_bytes, c->url);
        break;
    case in_stat_decoding_audio_start_pts:
        if (debug_frame_level)
            elv_dbg("IN STAT UDP fd=%d, audio start PTS=%"PRId64", url=%s", fd, c->decoding_start_pts, c->url);
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->decoding_start_pts);
        break;
    case in_stat_decoding_video_start_pts:
        if (debug_frame_level)
            elv_dbg("IN STAT UDP fd=%d, video start PTS=%"PRId64", url=%s", fd, c->decoding_start_pts, c->url);
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->decoding_start_pts);
        break;
    case in_stat_audio_frame_read:
        if (debug_frame_level)
            elv_dbg("IN STAT UDP fd=%d, audio frame read=%"PRId64", url=%s", fd, c->audio_frames_read, c->url);
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->audio_frames_read);
        break;
    case in_stat_video_frame_read:
        if (debug_frame_level)
            elv_dbg("IN STAT UDP fd=%d, video frame read=%"PRId64", url=%s", fd, c->video_frames_read, c->url);
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->video_frames_read);
        break;
    case in_stat_first_keyframe_pts:
        if (debug_frame_level)
            elv_dbg("IN STAT UDP fd=%d, first keyframe PTS=%"PRId64", url=%s", fd, c->first_key_frame_pts, c->url);
        rc = AVPipeStatInput(fd, stream_index, stat_type, &c->first_key_frame_pts);
        break;
    case in_stat_data_scte35:
        if (debug_frame_level)
            elv_dbg("IN STAT UDP SCTE35 fd=%d, stat_type=%d, url=%s", fd, stat_type, c->url);
        rc = AVPipeStatInput(fd, stream_index, stat_type, c->data);
        break;
    default:
        elv_err("IN STAT UDP fd=%d, invalid input stat=%d, url=%s", stat_type, c->url);
        return 1;
    }

    return rc;
}


static int
out_opener(
    const char *url,
    ioctx_t *outctx)
{
    ioctx_t *inctx = outctx->inctx;
    xcparams_t *xcparams = inctx->params;
    int64_t fd;
    int64_t h;

    h = *((int64_t *)(inctx->opaque));

    /* Allocate the buffers. The data will be copied to the buffers */
    outctx->bufsz = AVIO_OUT_BUF_SIZE;
    outctx->buf = (unsigned char *)av_malloc(outctx->bufsz); /* Must be malloc'd - will be realloc'd by avformat */

    fd = AVPipeOpenOutput(h, outctx->stream_index, outctx->seg_index, outctx->pts, outctx->type);
    if (xcparams && xcparams->debug_frame_level)
        elv_dbg("OUT out_opener outctx=%p, fd=%"PRId64", url=%s", outctx, fd, inctx->url);
    if (fd < 0) {
        elv_err("AVPIPE OUT OPEN failed stream_index=%d, seg_index=%d, type=%d, url=%s",
            outctx->stream_index, outctx->seg_index, outctx->type, inctx->url);
        return -1;
    }

    outctx->opaque = (int64_t *) malloc(sizeof(int64_t));
    *((int64_t *)(outctx->opaque)) = fd;

    return 0;
}

static int
out_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    ioctx_t *inctx = outctx->inctx;
    elv_err("OUT READ called, url=%s", inctx->url);
    return 0;
}

static int
out_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    ioctx_t *inctx = outctx->inctx;
    xcparams_t *xcparams = inctx->params;
    int64_t h = *((int64_t *)(inctx->opaque));
    int64_t fd = *(int64_t *)outctx->opaque;
    int bwritten = AVPipeWriteOutput(h, fd, buf, buf_size);
    if (bwritten >= 0) {
        outctx->written_bytes += bwritten;
        outctx->write_pos += bwritten;
    }

    if ((outctx->type == avpipe_video_fmp4_segment && 
        outctx->written_bytes - outctx->write_reported > VIDEO_BYTES_WRITE_REPORT) ||
        (outctx->type == avpipe_audio_fmp4_segment &&
        outctx->written_bytes - outctx->write_reported > AUDIO_BYTES_WRITE_REPORT)) {
        out_stat(opaque, outctx->stream_index, out_stat_bytes_written);
        outctx->write_reported = outctx->written_bytes;
    }

    if (xcparams && xcparams->debug_frame_level)
        elv_dbg("OUT WRITE stream_index=%d, fd=%"PRId64", size=%d, written=%d, pos=%d, total=%d",
            outctx->stream_index, fd, buf_size, bwritten, outctx->write_pos, outctx->written_bytes);

    return buf_size;
}

static int64_t
out_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    ioctx_t *inctx = outctx->inctx;
    int64_t h = *((int64_t *)(inctx->opaque));
    int64_t fd = *(int64_t *)outctx->opaque;
    int64_t rc = AVPipeSeekOutput(h, fd, offset, whence);
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

    elv_dbg("OUT SEEK fd=%"PRId64", offset=%d, whence=%d, rc=%"PRId64, fd, offset, whence, rc);

    return rc;
}

static int
out_closer(
    ioctx_t *outctx)
{
    if (!outctx)
        return -1;

    int64_t fd = *(int64_t *)outctx->opaque;
    ioctx_t *inctx = outctx->inctx;
    int64_t h = *((int64_t *)(inctx->opaque));
    int rc = AVPipeCloseOutput(h, fd);
    free(outctx->opaque);
    return rc;
}

static int
out_stat(
    void *opaque,
    int stream_index,
    avp_stat_t stat_type)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    ioctx_t *inctx = outctx->inctx;
    int64_t h;
    int64_t fd;
    int64_t rc = 0;
    avpipe_buftype_t buftype;

    /* Some error happened and fd is not set */
    if (!outctx || !outctx->opaque || !inctx || !inctx->opaque) {
        return 0;
    }

    h = *((int64_t *)(inctx->opaque));
    buftype = outctx->type;

    fd = *((int64_t *)(outctx->opaque));
    switch (stat_type) {
    case out_stat_bytes_written:
        rc = AVPipeStatOutput(h, fd, stream_index, buftype, stat_type, &outctx->written_bytes);
        break;
    case out_stat_encoding_end_pts:
        if (buftype == avpipe_audio_segment ||
            buftype == avpipe_audio_fmp4_segment)
            rc = AVPipeStatOutput(h, fd, stream_index, buftype, stat_type, &outctx->encoder_ctx->audio_last_pts_sent_encode);
        else
            rc = AVPipeStatOutput(h, fd, stream_index, buftype, stat_type, &outctx->encoder_ctx->video_last_pts_sent_encode);
        break;
    case out_stat_start_file:
        rc = AVPipeStatOutput(h, fd, stream_index, buftype, stat_type, &outctx->seg_index);
        break;
    case out_stat_end_file:
        rc = AVPipeStatOutput(h, fd, stream_index, buftype, stat_type, &outctx->seg_index);
        break;
    case out_stat_frame_written:
        {
            encoding_frame_stats_t encoding_frame_stats = {
                .total_frames_written = outctx->total_frames_written,
                .frames_written = outctx->frames_written,
            };
            rc = AVPipeStatOutput(h, fd, stream_index, buftype, stat_type, &encoding_frame_stats);
        }
        break;
    default:
        break;
    }
    return rc;
}

void
set_loggers()
{
    elv_set_log_func(elv_log_log, CLog);
    elv_set_log_func(elv_log_debug, CDebug);
    elv_set_log_func(elv_log_warning, CWarn);
    elv_set_log_func(elv_log_error, CError);
}

static void
init_tx_module()
{
    static int initialized = 0;

    if (initialized)
        return;
    srand(time(0));
    initialized = 1;
}

/*
 * Puts xctx in the tx table and returns the handle to xctx entry.
 * If tx table is full, it would return -1.
 */
static int32_t
xc_table_put(
    xctx_t *xctx)
{
    xc_entry_t *txe = NULL;
    xctx->index = -1;

    pthread_mutex_lock(&tx_mutex);
    for (int i=0; i<MAX_TX; i++) {
        if (xc_table[i] == NULL) {
            txe = calloc(1, sizeof(xc_entry_t));
            txe->handle = rand();
            if (txe->handle < 0)
                txe->handle = (-1) * txe->handle;
            txe->xctx = xctx;
            xc_table[i] = txe;
            xctx->index = i;
            xctx->handle = txe->handle;
            break;
        }
    }
    pthread_mutex_unlock(&tx_mutex);

    elv_dbg("xc_table_put handle=%d, url=%s", txe->handle, xctx->inctx->url);
    if (txe != NULL)
        return txe->handle;
    return -1;
}

static xc_entry_t*
xc_table_find(
    int32_t handle)
{
    pthread_mutex_lock(&tx_mutex);
    for (int i=0; i<MAX_TX; i++) {
        if (xc_table[i] != NULL && xc_table[i]->handle == handle) {
            pthread_mutex_unlock(&tx_mutex);
            return xc_table[i];
        }
    }
    pthread_mutex_unlock(&tx_mutex);

    return NULL;
}

static void
xc_table_free(
    int32_t handle)
{
    elv_dbg("xc_table_free handle=%d", handle);
    pthread_mutex_lock(&tx_mutex);
    for (int i=0; i<MAX_TX; i++) {
        if (xc_table[i] != NULL && xc_table[i]->handle == handle) {
            if (xc_table[i]->xctx->index == i) {
                free(xc_table[i]);
                xc_table[i] = NULL;
            } else
                elv_err("xc_table_free index=%d doesn't match with handle=%d at %d",
                    xc_table[i]->xctx->index, handle, i);
            pthread_mutex_unlock(&tx_mutex);
            return;
        }
    }
    pthread_mutex_unlock(&tx_mutex);
}

static int
xc_table_cancel(
    int32_t handle)
{
    int rc = eav_success;
    elv_dbg("xc_table_cancel handle=%d", handle);
    pthread_mutex_lock(&tx_mutex);
    for (int i=0; i<MAX_TX; i++) {
        if (xc_table[i] != NULL && xc_table[i]->handle == handle) {
            xctx_t *xctx = xc_table[i]->xctx;

            if (xctx->index == i) {
                xctx->decoder_ctx.cancelled = 1;
                xctx->encoder_ctx.cancelled = 1;
                /* If there is a UDP thread running wait for it to be finished */
                if ( xctx->inctx && xctx->inctx->utid ) {
                    xctx->inctx->closed = 1;
                    /* Close and purge the channel */
                    elv_channel_close(xctx->inctx->udp_channel, 1);
                    pthread_join(xctx->inctx->utid, NULL);
                } 
            } else {
                elv_err("xc_table_cancel index=%d doesn't match with handle=%d at %d",
                    xc_table[i]->xctx->index, handle, i);
                rc = eav_xc_table;
            }
            pthread_mutex_unlock(&tx_mutex);
            elv_dbg("xc_table_cancel handle=%d done, rc=%d", handle, rc);
            return rc;
        }
    }
    pthread_mutex_unlock(&tx_mutex);
    elv_dbg("xc_table_cancel handle=%d not found, rc=%d", handle, rc);
    return rc;
}

static int
set_handlers(
    char *url,
    avpipe_io_handler_t **p_in_handlers,
    avpipe_io_handler_t **p_out_handlers)
{
    url_parser_t url_parser;

    if (url && parse_url(url, &url_parser)) {
        elv_err("Failed to parse input url=%s", url);
        return eav_param;
    }

    /*
     * If input url is a UDP set/overwrite the default UDP input handlers.
     * No need for the client code to set/specify the input handlers when the input is UDP.
     */
    if (!strcmp(url_parser.protocol, "udp") && p_in_handlers) {
        avpipe_io_handler_t *in_handlers = (avpipe_io_handler_t *)calloc(1, sizeof(avpipe_io_handler_t));
        in_handlers->avpipe_opener = udp_in_opener;
        in_handlers->avpipe_closer = udp_in_closer;
        in_handlers->avpipe_reader = udp_in_read_packet;
        in_handlers->avpipe_writer = udp_in_write_packet;
        in_handlers->avpipe_seeker = udp_in_seek;
        in_handlers->avpipe_stater = udp_in_stat;
        *p_in_handlers = in_handlers;
    } else if (p_in_handlers) {
        avpipe_io_handler_t *in_handlers = (avpipe_io_handler_t *)calloc(1, sizeof(avpipe_io_handler_t));
        in_handlers->avpipe_opener = in_opener;
        in_handlers->avpipe_closer = in_closer;
        in_handlers->avpipe_reader = in_read_packet;
        in_handlers->avpipe_writer = in_write_packet;
        in_handlers->avpipe_seeker = in_seek;
        in_handlers->avpipe_stater = in_stat;
        *p_in_handlers = in_handlers;
    }

    if (p_out_handlers) {
        avpipe_io_handler_t *out_handlers = (avpipe_io_handler_t *)calloc(1, sizeof(avpipe_io_handler_t));
        out_handlers->avpipe_opener = out_opener;
        out_handlers->avpipe_closer = out_closer;
        out_handlers->avpipe_reader = out_read_packet;
        out_handlers->avpipe_writer = out_write_packet;
        out_handlers->avpipe_seeker = out_seek;
        out_handlers->avpipe_stater = out_stat;
        *p_out_handlers = out_handlers;
    }

    free_parsed_url(&url_parser);

    return eav_success;
}

/*
 * Obtains a handle that refers to an initialized trasncoding session with specified params.
 * If initialization is successfull it return eav_success, otherwise it returns corresponding error code.
 */
int32_t
xc_init(
    xcparams_t *params,
    int32_t *handle)
{
    xctx_t *xctx = NULL;
    int64_t rc = 0;
    uint32_t h;
    avpipe_io_handler_t *in_handlers = NULL;
    avpipe_io_handler_t *out_handlers = NULL;

    *handle = -1;
    if (!params || !params->url || params->url[0] == '\0' )
        return eav_param;

    init_tx_module();

    connect_ffmpeg_log();
    if ((rc = set_handlers(params->url, &in_handlers, &out_handlers)) != eav_success) {
        goto end_tx_init;
    }

    if ((rc = avpipe_init(&xctx, in_handlers, out_handlers, params)) != eav_success) {
        goto end_tx_init;
    }

    if ((h = xc_table_put(xctx)) < 0) {
        elv_err("xc_init xc_table is full, cancelling transcoding %s", params->url);
        rc = eav_xc_table;
        goto end_tx_init;
    }

    xctx->in_handlers = in_handlers; // PENDING(SS) already done in avpipe_init
    xctx->out_handlers = out_handlers;
    xctx->associate_thread = AssociateCThreadWithHandle;

    *handle = h;
    return eav_success;

end_tx_init:
    avpipe_fini(&xctx);

    return rc;
}

int
xc_run(
    int32_t handle)
{
    int rc = 0;
    xc_entry_t *xe = xc_table_find(handle);

    if (!xe) {
        elv_err("tx_run invalid handle=%d", handle);
        return eav_param;
    }

    xctx_t *xctx = xe->xctx;
    if ((rc = avpipe_xc(xctx, 0)) != eav_success) {
        if (rc != eav_cancelled)
            elv_err("Error in transcoding, handle=%d, err=%d", handle, rc);
        goto end_tx;
    }

end_tx:
    xc_table_free(handle);
    avpipe_fini(&xctx);

    return rc;
}

int
xc_cancel(
    int32_t handle)
{ 
    return xc_table_cancel(handle);
}

/*
 * 1) Initializes avpipe with appropriate parameters.
 * 2) Invokes avpipe trnascoding.
 * 3) Releases avpipe resources.
 */
int
xc(
    xcparams_t *params)
{
    xctx_t *xctx = NULL;
    int rc = 0;
    avpipe_io_handler_t *in_handlers;
    avpipe_io_handler_t *out_handlers;

    if (!params || !params->url || params->url[0] == '\0' )
        return eav_param;

    // Note: If log handler functions are set, log levels set through
    //       av_log_set_level and elv_set_log_level are ignored
    //av_log_set_level(AV_LOG_DEBUG);
    connect_ffmpeg_log();
    //elv_set_log_level(elv_log_debug);

    set_handlers(params->url, &in_handlers, &out_handlers);

    if ((rc = avpipe_init(&xctx, in_handlers, out_handlers, params)) != eav_success) {
        goto end_tx;
    }

    xctx->handle = GenerateAndRegisterHandle();
    xctx->associate_thread = AssociateCThreadWithHandle;

    if ((rc = avpipe_xc(xctx, 0)) != eav_success) {
        elv_err("Transcoding failed url=%s, rc=%d", params->url, rc);
        goto end_tx;
    }

end_tx:
    avpipe_fini(&xctx);

    return rc;
}

static int
in_mux_opener(
    const char *url,
    ioctx_t *inctx)
{
    int64_t size;
    char *out_filename = inctx->in_mux_ctx->out_filename;

    inctx->opaque = (void *) calloc(1, sizeof(int64_t));
    if (url != NULL)
        inctx->url = strdup(url);
    else
        /* Default file input would be assumed to be mp4 */
        inctx->url = "bogus.mp4";

    int64_t fd = AVPipeOpenMuxInput((char *) out_filename, (char *) url, &size);
    if (fd <= 0 )
        return -1;

    if (size > 0)
        inctx->sz = size;
    elv_dbg("IN MUX OPEN url=%s, fd=%"PRId64", size=%"PRId64, url, fd, size);

    *((int64_t *)(inctx->opaque)) = fd;
    return 0;
}


static int
in_mux_read_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *c = (ioctx_t *)opaque;
    xcparams_t *xcparams = c->params;
    int debug_frame_level = (xcparams != NULL) ? xcparams->debug_frame_level : 0;
    int r;
    int64_t fd;

read_next_input:
    /* This means the input file is not opened yet, so open the input file */
    if (!c->opaque) {
        io_mux_ctx_t *in_mux_ctx = c->in_mux_ctx;
        int index = c->in_mux_index;
        int i;
        char *filepath;

        /* index 0 means video */
        if (index == 0) {
            /* Reached end of videos */
            if (in_mux_ctx->video.index >= in_mux_ctx->video.n_parts)
                return AVERROR_EOF;
            filepath = in_mux_ctx->video.parts[in_mux_ctx->video.index];
            in_mux_ctx->video.index++;
        } else if (index <= in_mux_ctx->audio_count) {
            if (in_mux_ctx->audios[index-1].index >= in_mux_ctx->audios[index-1].n_parts)
                return AVERROR_EOF;
            filepath = in_mux_ctx->audios[index-1].parts[in_mux_ctx->audios[index-1].index];
            in_mux_ctx->audios[index-1].index++;
        } else if (index <= in_mux_ctx->audio_count+in_mux_ctx->caption_count) {
            i = index - in_mux_ctx->audio_count - 1;
            if (in_mux_ctx->captions[i].index >= in_mux_ctx->captions[i].n_parts)
                return AVERROR_EOF;
            filepath = in_mux_ctx->captions[i].parts[in_mux_ctx->captions[i].index];
            in_mux_ctx->captions[i].index++;
        } else {
            elv_err("in_mux_read_packet invalid index=%d", index);
            return -1;
        }

        c->opaque = (int *) calloc(1, sizeof(int));
        if (in_mux_opener(filepath, c) < 0) {
            elv_err("in_mux_read_packet failed to open file=%s", filepath);
            return -1;
        }
        fd = *((int64_t *)(c->opaque));

#if 0
        /* PENDING(RM) complete this for multiple mez inputs */ 
        if (new_video)
            in_mux_ctx->video.header_size = read_header(fd, in_mux_ctx->mux_type);
        else if (new_audio)
            in_mux_ctx->audios[index-1].header_size = read_header(fd, in_mux_ctx->mux_type);
#endif

        if (debug_frame_level) {
            elv_dbg("IN MUX READ opened new file filepath=%s, fd=%d", filepath, fd);
            if (strstr(filepath, "init")) {
                // The read in which this log happens only contains data from new continuity
                elv_dbg("IN MUX READ INIT open filepath=%s, pos=%d", filepath, c->read_bytes);
            }
        }
    }

    fd = *((int64_t *)(c->opaque));
    r = AVPipeReadInput(fd, buf, buf_size);
    if (debug_frame_level)
        elv_dbg("IN MUX READ index=%d, buf=%p buf_size=%d fd=%d, r=%d",
            c->in_mux_index, buf, buf_size, fd, r);

    /* If it is EOF, read the next input file */
    if (r == 0) {
        if (debug_frame_level)
            elv_dbg("IN MUX READ closing file fd=%d", fd);
        in_closer(c);
        free(c->opaque);
        c->opaque = NULL;
        goto read_next_input;
    }

    if (r > 0) {
        c->read_bytes += r;
        c->read_pos += r;
    }

    if (c->read_bytes - c->read_reported > BYTES_READ_REPORT) {
        /* Pass stream_index 0 (stream_index has no meaning for in_stat_bytes_read) */
        in_stat(opaque, 0, in_stat_bytes_read);
        c->read_reported = c->read_bytes;
    }

    if (debug_frame_level)
        elv_dbg("IN MUX READ read=%d pos=%"PRId64" total=%"PRId64, r, c->read_pos, c->read_bytes);
    return r > 0 ? r : -1;
}

static int64_t
in_mux_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *c = (ioctx_t *)opaque;
    xcparams_t *xcparams = (c != NULL) ? c->params : NULL;
    if (xcparams != NULL && xcparams->debug_frame_level)
        elv_dbg("IN MUX SEEK index=%d, offset=%"PRId64" whence=%d", c->in_mux_index, offset, whence);
    return -1;
}

static int
out_mux_opener(
    const char *url,
    ioctx_t *outctx)
{
    int64_t fd;
    ioctx_t *inctx = outctx->inctx;
    xcparams_t *xcparams = (inctx != NULL) ? inctx->params : NULL;

    /* Allocate the buffers. The data will be copied to the buffers */
    outctx->bufsz = AVIO_OUT_BUF_SIZE;
    outctx->buf = (unsigned char *)av_malloc(outctx->bufsz); /* Must be malloc'd - will be realloc'd by avformat */

    fd = AVPipeOpenMuxOutput((char *) url, outctx->type);
    if (xcparams != NULL && xcparams->debug_frame_level)
        elv_dbg("OUT out_mux_opener outctx=%p, fd=%"PRId64, outctx, fd);
    if (fd < 0) {
        elv_err("AVPIPE OUT MUX OPEN failed, type=%d", outctx->type);
        return -1;
    }

    outctx->opaque = (int *) malloc(sizeof(int64_t));
    *((int64_t *)(outctx->opaque)) = fd;

    return 0;
}

static int
out_mux_closer(
    ioctx_t *outctx)
{
    int64_t fd = *(int64_t *)outctx->opaque;
    int rc = AVPipeCloseMuxOutput(fd);
    free(outctx->opaque);
    free(outctx->buf);
    return rc;
}


static int
out_mux_write_packet(
    void *opaque,
    uint8_t *buf,
    int buf_size)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    ioctx_t *inctx = outctx->inctx;
    xcparams_t *xcparams = (inctx != NULL) ? inctx->params : NULL;
    int64_t fd = *(int64_t *)outctx->opaque;
    int bwritten = AVPipeWriteMuxOutput(fd, buf, buf_size);
    if (bwritten >= 0) {
        outctx->written_bytes += bwritten;
        outctx->write_pos += bwritten;
    }

    if (xcparams != NULL && xcparams->debug_frame_level)
        elv_dbg("OUT MUX WRITE fd=%"PRId64", size=%d written=%d pos=%d total=%d",
            fd, buf_size, bwritten, outctx->write_pos, outctx->written_bytes);

    return bwritten;
}

static int64_t
out_mux_seek(
    void *opaque,
    int64_t offset,
    int whence)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    ioctx_t *inctx = outctx->inctx;
    xcparams_t *xcparams = (inctx != NULL) ? inctx->params : NULL;
    int64_t fd = *(int64_t *)outctx->opaque;
    int64_t rc = AVPipeSeekMuxOutput(fd, offset, whence);
    if (xcparams != NULL && xcparams->debug_frame_level)
        elv_dbg("OUT MUX SEEK fd=%"PRId64" offset=%d whence=%d", fd, offset, whence);
    return rc;
}

static int
out_mux_stat(
    void *opaque,
    int stream_index,
    avp_stat_t stat_type)
{
    ioctx_t *outctx = (ioctx_t *)opaque;
    int64_t fd = *(int64_t *)outctx->opaque;
    int64_t rc = 0;
    avpipe_buftype_t buftype = outctx->type;

    switch (stat_type) {
    case out_stat_bytes_written:
        rc = AVPipeStatMuxOutput(fd, stream_index, stat_type, &outctx->written_bytes);
        break;
    case out_stat_encoding_end_pts:
        rc = AVPipeStatMuxOutput(fd, stream_index, stat_type, &outctx->encoder_ctx->video_last_pts_sent_encode);
        break;
    default:
        break;
    }
    return rc;
}


static void
set_mux_handlers(
    avpipe_io_handler_t **p_in_handlers,
    avpipe_io_handler_t **p_out_handlers)
{
    if (p_in_handlers) {
        avpipe_io_handler_t *in_handlers = (avpipe_io_handler_t *)calloc(1, sizeof(avpipe_io_handler_t));
        in_handlers->avpipe_opener = in_mux_opener;
        in_handlers->avpipe_closer = in_closer;
        in_handlers->avpipe_reader = in_mux_read_packet;
        in_handlers->avpipe_writer = in_write_packet;
        in_handlers->avpipe_seeker = in_mux_seek;
        in_handlers->avpipe_stater = in_stat;
        *p_in_handlers = in_handlers;
    }

    if (p_out_handlers) {
        avpipe_io_handler_t *out_handlers = (avpipe_io_handler_t *)calloc(1, sizeof(avpipe_io_handler_t));
        out_handlers->avpipe_opener = out_mux_opener;
        out_handlers->avpipe_closer = out_mux_closer;
        out_handlers->avpipe_reader = out_read_packet;
        out_handlers->avpipe_writer = out_mux_write_packet;
        out_handlers->avpipe_seeker = out_mux_seek;
        out_handlers->avpipe_stater = out_mux_stat;
        *p_out_handlers = out_handlers;
    }
}

/*
 * filename is the output url/filename that uniquely identifies the result of muxer.
 */
int
mux(
    xcparams_t *params)
{
    io_mux_ctx_t *in_mux_ctx = NULL;
    xctx_t *xctx = NULL;
    int rc = 0;
    avpipe_io_handler_t *in_handlers;
    avpipe_io_handler_t *out_handlers;

    if (!params || !params->url || params->url[0] == '\0' )
        return eav_param;

    connect_ffmpeg_log();

    set_mux_handlers(&in_handlers, &out_handlers);
    in_mux_ctx = (io_mux_ctx_t *)calloc(1, sizeof(io_mux_ctx_t));

    if ((rc = avpipe_init_muxer(&xctx,
        in_handlers, in_mux_ctx, out_handlers, params)) != eav_success) {
        elv_err("Initializing muxer failed, url=%s", params->url);
        goto end_mux;
    }

    xctx->handle = GenerateAndRegisterHandle();
    xctx->associate_thread = AssociateCThreadWithHandle;

    if ((rc = avpipe_mux(xctx)) != eav_success) {
        elv_err("Muxing failed");
        goto end_mux;
    }

end_mux:
    elv_dbg("Releasing all the muxing resources, url=%s", params->url);
    avpipe_mux_fini(&xctx);
    free(in_mux_ctx);

    return rc;
}

const char *
get_pix_fmt_name(
    int pix_fmt)
{
    return av_get_pix_fmt_name((enum AVPixelFormat) pix_fmt);
}

const char *
get_profile_name(
    int codec_id,
    int profile)
{
    return avcodec_profile_name((enum AVCodecID) codec_id, profile);
}

int
probe(
    xcparams_t *params,
    xcprobe_t **xcprobe,
    int *n_streams)
{
    avpipe_io_handler_t *in_handlers = NULL;
    xcprobe_t *probes;
    int rc;

    if (!params || !params->url || params->url[0] == '\0' )
        return eav_param;

    rc = set_handlers(params->url, &in_handlers, NULL);
    if (rc != eav_success)
        goto end_probe;

    rc = avpipe_probe(in_handlers, params, &probes, n_streams);
    if (rc != eav_success)
        goto end_probe;

    *xcprobe = probes;

end_probe:
    elv_dbg("Releasing probe resources, url=%s", params->url);
    free(in_handlers);
    return rc;
}
