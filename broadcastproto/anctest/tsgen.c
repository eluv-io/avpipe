#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include <libavcodec/packet.h>
#include <libavformat/avformat.h>

#include "tsgen.h"

#define TSGEN_MAX_STREAMS 16

struct tsgen_t {
    AVFormatContext *in;
    AVFormatContext *out;
    int             vin;                        /* input video stream index */
    int             map[TSGEN_MAX_STREAMS];     /* input stream index -> output stream index, -1 = not copied */
    AVStream        *dout;
    AVPacket        *pkt;
    int             header_written;
};

static const AVRational tb90k = {1, 90000};

static void
tsgen_free(tsgen_t *g)
{
    if (!g)
        return;
    if (g->out) {
        if (g->out->pb)
            avio_closep(&g->out->pb);
        avformat_free_context(g->out);
    }
    if (g->in)
        avformat_close_input(&g->in);
    av_packet_free(&g->pkt);
    free(g);
}

tsgen_t *
tsgen_open(const char *src, const char *dst, int unmarked)
{
    tsgen_t *g = calloc(1, sizeof(*g));
    int rc;

    if (!g)
        return NULL;
    for (int i = 0; i < TSGEN_MAX_STREAMS; i++)
        g->map[i] = -1;
    if ((rc = avformat_open_input(&g->in, src, NULL, NULL)) < 0 ||
        (rc = avformat_find_stream_info(g->in, NULL)) < 0 ||
        (rc = g->vin = av_find_best_stream(g->in, AVMEDIA_TYPE_VIDEO, -1, -1, NULL, 0)) < 0 ||
        (rc = avformat_alloc_output_context2(&g->out, NULL, "mpegts", dst)) < 0)
        goto fail;

    /* Every video and audio stream is copied, in input order, so the TS has the same stream layout as the clip */
    for (unsigned i = 0; i < g->in->nb_streams && i < TSGEN_MAX_STREAMS; i++) {
        AVStream *in = g->in->streams[i];
        if (in->codecpar->codec_type != AVMEDIA_TYPE_VIDEO && in->codecpar->codec_type != AVMEDIA_TYPE_AUDIO)
            continue;
        AVStream *out = avformat_new_stream(g->out, NULL);
        if (!out) {
            rc = AVERROR(ENOMEM);
            goto fail;
        }
        if ((rc = avcodec_parameters_copy(out->codecpar, in->codecpar)) < 0)
            goto fail;
        out->codecpar->codec_tag = 0;
        out->time_base = tb90k;
        g->map[i] = out->index;
    }

    g->dout = avformat_new_stream(g->out, NULL);
    g->pkt = av_packet_alloc();
    if (!g->dout || !g->pkt) {
        rc = AVERROR(ENOMEM);
        goto fail;
    }
    g->dout->codecpar->codec_type = AVMEDIA_TYPE_DATA;
    /* An unknown data codec is muxed as private data with no registration descriptor */
    g->dout->codecpar->codec_id = unmarked ? AV_CODEC_ID_BIN_DATA : AV_CODEC_ID_SMPTE_2038;
    g->dout->time_base = tb90k;

    /* No interleave limit: the media is held until the data stream has packets, which come after all of it */
    g->out->max_interleave_delta = 0;

    if ((rc = avio_open(&g->out->pb, dst, AVIO_FLAG_WRITE)) < 0 ||
        (rc = avformat_write_header(g->out, NULL)) < 0)
        goto fail;
    g->header_written = 1;
    return g;

fail:
    fprintf(stderr, "tsgen_open: %s\n", av_err2str(rc));
    tsgen_free(g);
    return NULL;
}

int
tsgen_next_video(tsgen_t *g, int64_t *pts, int64_t *dts)
{
    for (;;) {
        int rc = av_read_frame(g->in, g->pkt);
        if (rc == AVERROR_EOF)
            return 0;
        if (rc < 0) {
            fprintf(stderr, "tsgen_next_video: read: %s\n", av_err2str(rc));
            return rc;
        }
        int in_index = g->pkt->stream_index;
        int out_index = in_index < TSGEN_MAX_STREAMS ? g->map[in_index] : -1;
        if (out_index < 0) {
            av_packet_unref(g->pkt);
            continue;
        }
        int is_video = in_index == g->vin;
        av_packet_rescale_ts(g->pkt, g->in->streams[in_index]->time_base, g->out->streams[out_index]->time_base);
        g->pkt->stream_index = out_index;
        g->pkt->pos = -1;
        if (is_video) {
            *pts = g->pkt->pts;
            *dts = g->pkt->dts;
        }
        /* The muxer converts avcC extradata to Annex B itself (its bitstream check) */
        rc = av_interleaved_write_frame(g->out, g->pkt);
        if (rc < 0) {
            fprintf(stderr, "tsgen_next_video: write: %s\n", av_err2str(rc));
            return rc;
        }
        if (is_video)
            return 1;
    }
}

int
tsgen_write_anc(tsgen_t *g, int64_t pts, const uint8_t *data, int size)
{
    AVPacket *p = av_packet_alloc();
    uint8_t *sd;
    int rc;

    if (!p)
        return AVERROR(ENOMEM);
    if ((rc = av_new_packet(p, size)) < 0) {
        av_packet_free(&p);
        return rc;
    }
    memcpy(p->data, data, size);
    p->pts = p->dts = pts;
    p->stream_index = g->dout->index;
    /* private_stream_1, the PES stream_id ST 2038 specifies; the muxer's default for data is 0xFC */
    sd = av_packet_new_side_data(p, AV_PKT_DATA_MPEGTS_STREAM_ID, 1);
    if (!sd) {
        av_packet_free(&p);
        return AVERROR(ENOMEM);
    }
    sd[0] = 0xbd;
    rc = av_interleaved_write_frame(g->out, p);
    av_packet_free(&p);
    if (rc < 0)
        fprintf(stderr, "tsgen_write_anc: pts=%lld: %s\n", (long long)pts, av_err2str(rc));
    return rc;
}

int
tsgen_close(tsgen_t *g)
{
    int rc = 0;

    if (!g)
        return 0;
    if (g->header_written && (rc = av_write_trailer(g->out)) < 0)
        fprintf(stderr, "tsgen_close: trailer: %s\n", av_err2str(rc));
    tsgen_free(g);
    return rc;
}
