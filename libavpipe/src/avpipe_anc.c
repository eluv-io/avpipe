/*
 * avpipe_anc.c: scan of a SMPTE ST 2038 ancillary data stream through libavformat (see avpipe_anc.h).
 */
#include <stdlib.h>
#include <string.h>

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>

#include "avpipe_anc.h"
#include "elv_log.h"

static int
anc_scan_find_stream(AVFormatContext *fc, int anc_stream_id)
{
    for (unsigned i = 0; i < fc->nb_streams; i++) {
        AVStream *st = fc->streams[i];
        if (st->codecpar->codec_type != AVMEDIA_TYPE_DATA)
            continue;
        if (st->codecpar->codec_id == AV_CODEC_ID_SMPTE_2038)
            return (int) i;
        if (anc_stream_id > 0 && st->id == anc_stream_id)
            return (int) i;
    }
    return -1;
}

int
anc_scan_open(const char *url, int anc_stream_id, anc_scan_t **out)
{
    anc_scan_t *s = calloc(1, sizeof(*s));
    int rc;

    *out = NULL;
    if (!s)
        return -1;
    if ((rc = avformat_open_input(&s->fc, url, NULL, NULL)) < 0 ||
        (rc = avformat_find_stream_info(s->fc, NULL)) < 0) {
        elv_err("anc_scan_open: %s: %s", url, av_err2str(rc));
        anc_scan_close(s);
        return -1;
    }
    s->stream_index = anc_scan_find_stream(s->fc, anc_stream_id);
    if (s->stream_index < 0) {
        anc_scan_close(s);
        return -2;
    }
    s->pkt = av_packet_alloc();
    if (!s->pkt) {
        anc_scan_close(s);
        return -1;
    }
    AVStream *st = s->fc->streams[s->stream_index];
    s->stream_id = st->id;
    strncpy(s->codec_name, avcodec_get_name(st->codecpar->codec_id), sizeof(s->codec_name) - 1);
    av_fourcc_make_string(s->codec_tag, st->codecpar->codec_tag);
    *out = s;
    return 0;
}

int
anc_scan_next(anc_scan_t *s, anc_packet_t *pkt)
{
    for (;;) {
        av_packet_unref(s->pkt);
        int rc = av_read_frame(s->fc, s->pkt);
        if (rc == AVERROR_EOF)
            return 0;
        if (rc < 0) {
            elv_err("anc_scan_next: %s", av_err2str(rc));
            return rc;
        }
        if (s->pkt->stream_index != s->stream_index)
            continue;
        AVRational tb = s->fc->streams[s->stream_index]->time_base;
        *pkt = (anc_packet_t){
            .pts = s->pkt->pts,
            .dts = s->pkt->dts,
            .time_base_num = tb.num,
            .time_base_den = tb.den,
            .data = s->pkt->data,
            .size = s->pkt->size,
        };
        return 1;
    }
}

void
anc_scan_close(anc_scan_t *s)
{
    if (!s)
        return;
    av_packet_free(&s->pkt);
    if (s->fc)
        avformat_close_input(&s->fc);
    free(s);
}
