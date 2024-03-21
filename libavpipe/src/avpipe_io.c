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

    elv_dbg("OUT elv_io_open url=%s", url);

    out_tracker_t *out_tracker = (out_tracker_t *) format_ctx->avpipe_opaque;
    avpipe_io_handler_t *out_handlers = out_tracker->out_handlers;

    if (strstr(url, "chunk")) {
        /* Regular segment */
        char *endptr;
        AVDictionaryEntry *stream_opt = av_dict_get(*options, "stream_index", 0, 0);
        ioctx_t *outctx = (ioctx_t *) calloc(1, sizeof(ioctx_t));

        /* The outctx is created after writing the first frame, so set frames_written to 1. */
        //outctx->frames_written = 1;
        outctx->encoder_ctx = out_tracker->encoder_ctx;
        outctx->stream_index = (int) strtol(stream_opt->value, &endptr, 10);
        outctx->url = strdup(url);
        assert(outctx->stream_index == 0 || outctx->stream_index == 1);
        if (out_tracker[outctx->stream_index].xc_type == xc_video)
            outctx->type = avpipe_video_segment;
        else
            outctx->type = avpipe_audio_segment;
        outctx->seg_index = out_tracker[outctx->stream_index].seg_index;
        out_tracker[outctx->stream_index].seg_index++;
        outctx->inctx = out_tracker[outctx->stream_index].inctx;

        if (out_handlers->avpipe_opener(url, outctx) < 0) {
            free(outctx);
            return -1;
        }

        AVIOContext *avioctx = avio_alloc_context(outctx->buf, outctx->bufsz, AVIO_FLAG_WRITE, (void *)outctx,
            out_handlers->avpipe_reader, out_handlers->avpipe_writer, out_handlers->avpipe_seeker);

        avioctx->seekable = 0;
        avioctx->direct = 1;
        (*pb) = avioctx;
        out_tracker[outctx->stream_index].last_outctx = outctx;

        elv_dbg("OUT elv_io_open stream_index=%d, seg_index=%d avioctx=%p, avioctx->opaque=%p, buf=%p, outctx=%p, outtracker->last_outctx=%p, outtracker->last_outctx=%p",
            outctx->stream_index, outctx->seg_index, avioctx, avioctx->opaque, avioctx->buffer, outctx, out_tracker[outctx->stream_index].last_outctx, out_tracker[outctx->stream_index].last_outctx);
    } else {
        ioctx_t *outctx = (ioctx_t *) calloc(1, sizeof(ioctx_t));
        outctx->stream_index = 0;
        outctx->encoder_ctx = out_tracker[outctx->stream_index].encoder_ctx;
        outctx->inctx = out_tracker[outctx->stream_index].inctx;
        outctx->seg_index = 0; // init segment has stream_index and seg_index = 0

        if (!url || url[0] == '\0') {
            outctx->type = avpipe_manifest;
        } else if (strstr(url, ".jpeg")) {
            // filename is [pts].jpeg; leave stream_index and seg_index at 0
            outctx->url = strdup(url);
            outctx->type = avpipe_image;
            if (sscanf(url, "%"PRId64".jp%*s", &outctx->pts) != 1) {
                elv_dbg("Invalid out url=%s", url);
                free(outctx);
                return -1;
            }
        } else {
            outctx->url = strdup(url);
            outctx->stream_index = 0;
            if (!strstr(url, "m3u8")) {
                int i = 0;
                while (i < strlen(url) && !isdigit(url[i]))
                    i++;
                if (i < strlen(url)) {
                    // Assumes a filename like segment%d-%05d.mp4
                    outctx->stream_index = url[i] - '0';
                }
            }
            outctx->encoder_ctx = out_tracker[outctx->stream_index].encoder_ctx;
            outctx->inctx = out_tracker[outctx->stream_index].inctx;
            elv_dbg("XXX stream_index=%d", outctx->stream_index);
            if (!strncmp(url + strlen(url) - 3, "mpd", 3)) {
                outctx->type = avpipe_manifest;
                outctx->seg_index = -1;     // Special index for manifest
            }
            else if (!strncmp(url, "master", 6)) {
                outctx->type = avpipe_master_m3u;
                outctx->seg_index = -1;     // Special index for manifest
            }
            else if (!strncmp(url, "media", 5)) {
                if (out_tracker[outctx->stream_index].xc_type == xc_video)
                    outctx->type = avpipe_video_m3u;
                else
                    outctx->type = avpipe_audio_m3u;
                outctx->seg_index = -1;     // Special index for manifest
            }
            else if (!strncmp(url, "init", 4)) {
                if (out_tracker[outctx->stream_index].xc_type == xc_video)
                    outctx->type = avpipe_video_init_stream;
                else
                    outctx->type = avpipe_audio_init_stream;
            }
            else if (!strncmp(url, "key.bin", 7)) {
                outctx->type = avpipe_aes_128_key;
                outctx->seg_index = -2;
            }
            else if (!strncmp(url, "mp4", 3)) {
                outctx->type = avpipe_mp4_stream;
            } else if (strstr(url, "fsegment")) {
                if (strstr(url, "fsegment-video"))
                    outctx->type = avpipe_video_fmp4_segment;
                else if (strstr(url, "fsegment-audio"))
                    outctx->type = avpipe_audio_fmp4_segment;
                else {
                    elv_dbg("Invalid out url=%s", url);
                    free(outctx);
                    return -1;
                }
                outctx->seg_index = out_tracker[outctx->stream_index].seg_index;
                out_tracker[outctx->stream_index].seg_index++;
                outctx->inctx = out_tracker[outctx->stream_index].inctx;
            } else if (!strncmp(url, "fmp4", 4)) {
                outctx->type = avpipe_fmp4_stream;
            } else if (strstr(url, "segment")) {
                outctx->type = avpipe_mp4_segment;
                outctx->seg_index = out_tracker[outctx->stream_index].seg_index;
                out_tracker[outctx->stream_index].seg_index++;
                outctx->inctx = out_tracker[outctx->stream_index].inctx;
            }
        }
 
        if (outctx->type == avpipe_mp4_segment ||
            outctx->type == avpipe_fmp4_stream ||
            outctx->type == avpipe_mp4_stream ||
            outctx->type == avpipe_video_fmp4_segment ||
            outctx->type == avpipe_audio_fmp4_segment)
            // not set for outctx->type == avpipe_image because elv_io_close will free outctx for each frame extracted
            out_tracker[outctx->stream_index].last_outctx = outctx;
        /* Manifest or init segments */
        if (out_handlers->avpipe_opener(url, outctx) < 0) {
            free(outctx);
            return -1;
        }

        AVIOContext *avioctx = avio_alloc_context(outctx->buf, outctx->bufsz, AVIO_FLAG_WRITE, (void *)outctx,
            out_handlers->avpipe_reader, out_handlers->avpipe_writer, out_handlers->avpipe_seeker);

        elv_dbg("OUT elv_io_open url=%s, type=%d, stream_index=%d, seg_index=%d, last_outctx=%p, buf=%p",
            url, outctx->type, outctx->stream_index, outctx->seg_index, out_tracker[outctx->stream_index].last_outctx, avioctx->buffer);

        /* libavformat expects seekable streams for mp4 */
        if (outctx->type == avpipe_mp4_stream || outctx->type == avpipe_mp4_segment)
            avioctx->seekable = 1;
        else
            avioctx->seekable = 0;

        /* If the stream is fragmented mp4, to avoid seek, direct flag must be zero */
        if (outctx->type == avpipe_fmp4_stream ||
            outctx->type == avpipe_video_fmp4_segment ||
            outctx->type == avpipe_audio_fmp4_segment)
            avioctx->direct = 0;
        else
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
    AVIOContext *avioctx = (AVIOContext *) pb;
    ioctx_t *outctx = (ioctx_t *)pb->opaque;
    avpipe_io_handler_t *out_handlers = NULL;

    if (out_tracker != NULL)
        out_handlers = out_tracker->out_handlers;

    elv_dbg("OUT elv_io_close url=%s, stream_index=%d, seg_index=%d avioctx=%p, avioctx->opaque=%p buf=%p outtracker[0]->last_outctx=%p, outtracker[1]->last_outctx=%p",
        outctx != NULL ? outctx->url : "", outctx != NULL ? outctx->stream_index : -1, outctx != NULL ? outctx->seg_index : -1, pb, pb->opaque, avioctx->buffer,
	    out_tracker != NULL ? out_tracker[0].last_outctx : 0, out_tracker != NULL ? out_tracker[1].last_outctx : 0);
    if (out_handlers) {
#if 0
        // PENDING (RM): this crashes with ffmpeg-4.4, needs to be fixed
        out_handlers->avpipe_stater(outctx, out_stat_encoding_end_pts);
#endif
        out_handlers->avpipe_closer(outctx);
    }
    if (outctx)
        free(outctx->url);
    free(outctx);
    pb->opaque = NULL;
    if (out_tracker)
        out_tracker->last_outctx = NULL;
    av_freep(&avioctx->buffer);
    avio_context_free(&avioctx);
    return;
}
