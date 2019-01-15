/*
 * avpipe_io.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libavutil/log.h>
#include <libavfilter/buffersink.h>
#include <libavfilter/buffersrc.h>

#include "avpipe_xc.h"
#include "avpipe_utils.h"
#include "elv_log.h"

#include <stdio.h>
#include <fcntl.h>
#include <assert.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <sys/uio.h>
#include <unistd.h>
#include <errno.h>
#include <ctype.h>


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
    int ret = 0;

    elv_dbg("OUT io_open url=%s", url);

    out_tracker_t *out_tracker = (out_tracker_t *) format_ctx->avpipe_opaque;
    avpipe_io_handler_t *out_handlers = out_tracker->out_handlers;

    if (strstr(url, "chunk")) {
        /* Regular segment */
        char *endptr;
        AVDictionaryEntry *stream_opt = av_dict_get(*options, "stream_index", 0, 0);
        ioctx_t *outctx = (ioctx_t *) calloc(1, sizeof(ioctx_t));

        outctx->type = avpipe_segment;
        outctx->stream_index = (int) strtol(stream_opt->value, &endptr, 10);
        assert(outctx->stream_index == 0 || outctx->stream_index == 1);
        out_tracker[outctx->stream_index].seg_index++;
        outctx->seg_index = out_tracker[outctx->stream_index].seg_index;

        if (out_handlers->avpipe_opener(url, outctx) < 0) {
            free(outctx);
            return -1;
        }

        AVIOContext *avioctx = avio_alloc_context(outctx->buf, outctx->sz, AVIO_FLAG_WRITE, (void *)outctx,
            out_handlers->avpipe_reader, out_handlers->avpipe_writer, out_handlers->avpipe_seeker);

        avioctx->seekable = 0;
        avioctx->direct = 1;
        (*pb) = avioctx;
        out_tracker[outctx->stream_index].last_outctx = outctx;

        elv_dbg("OUT open stream_index=%d, seg_index=%d avioctx=%p, avioctx->opaque=%p, outctx=%p, outtracker[0]->last_outctx=%p, outtracker[1]->last_outctx=%p",
            outctx->stream_index, outctx->seg_index, avioctx, avioctx->opaque, outctx, out_tracker[0].last_outctx, out_tracker[1].last_outctx);
    } else {

        ioctx_t *outctx = (ioctx_t *) calloc(1, sizeof(ioctx_t));
        outctx->stream_index = 0;

        if (!url || url[0] == '\0') {
            outctx->type = avpipe_manifest;
            outctx->seg_index = 0;      // Manifest file has stream_index and seg_index = 0
        } else {
            int i = 0;
            outctx->type = avpipe_init_stream;
            while (i < strlen(url) && !isdigit(url[i]))
                i++;
            if (i < strlen(url)) {
                outctx->stream_index = url[i] - '0';
            }
            outctx->seg_index = -1;     // Special index for init-stream0 and init-stream1
        }

        elv_dbg("OUT url=%s, type=%d", url, outctx->type);
        /* Manifest or init segments */
        if (out_handlers->avpipe_opener(url, outctx) < 0) {
            free(outctx);
            return -1;
        }

        AVIOContext *avioctx = avio_alloc_context(outctx->buf, outctx->bufsz, AVIO_FLAG_WRITE, (void *)outctx,
            out_handlers->avpipe_reader, out_handlers->avpipe_writer, out_handlers->avpipe_seeker);

        avioctx->seekable = 0;
        avioctx->direct = 1;
        (*pb) = avioctx;
    }

    return ret;
}

void
elv_io_close(
    struct AVFormatContext *format_ctx,
    AVIOContext *pb)
{
    out_tracker_t *out_tracker = (out_tracker_t *) format_ctx->avpipe_opaque;
    avpipe_io_handler_t *out_handlers = out_tracker->out_handlers;
    ioctx_t *outctx = (ioctx_t *)pb->opaque;

    elv_dbg("OUT close avioctx=%p, avioctx->opaque=%p outtracker[0]->last_outctx=%p, outtracker[1]->last_outctx=%p",
        pb, pb->opaque, out_tracker[0].last_outctx, out_tracker[1].last_outctx);
    out_handlers->avpipe_closer(outctx);
    free(outctx);
    return;
}
