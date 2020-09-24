/*
 * avpipe_utils.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libavutil/log.h>

#include "avpipe_utils.h"
#include "avpipe_xc.h"
#include "elv_log.h"

#include <sys/time.h>

const char *stream_type_str(
    coderctx_t *c,
    int idx)
{
    if (idx == c->video_stream_index)
        return "v";
    if (idx == c->audio_stream_index)
        return "a";
    return "u";
}

void
dump_frame(
    int is_audio,
    char *msg,
    int num,
    AVFrame *frame,
    int debug_frame_level)
{
    if (!debug_frame_level || !frame)
        return;

    elv_dbg("%s FRAME %s [%d] pts=%"PRId64" pkt_dts=%"PRId64" pkt_duration=%"PRId64" be_time_stamp=%"PRId64" key=%d pict_type=%d "
        "pkt_size=%d nb_samples=%d "
        "width=%d height=%d linesize=%d "
        "format=%d coded_pic_num=%d flags=%x "
        "\n", is_audio ? "AUDIO" : "VIDEO", msg, num,
        frame->pts, frame->pkt_dts, frame->pkt_duration, frame->best_effort_timestamp,
        frame->key_frame, frame->pict_type,
        frame->pkt_size, frame->nb_samples,
        frame->width, frame->height, frame->linesize[0],
        frame->format, frame->coded_picture_number,
        frame->flags
    );
}

void
dump_packet(
    int is_audio,
    const char *msg,
    AVPacket *p,
    int debug_frame_level)
{
    if (!debug_frame_level || !p)
        return;

    elv_dbg("%s PACKET %s pts=%"PRId64" dts=%"PRId64" duration=%"PRId64" pos=%"PRId64" size=%d stream_index=%d flags=%x\n",
        is_audio ? "AUDIO" : "VIDEO", msg,
        p->pts, p->dts, p->duration, p->pos, p->size, p->stream_index,
        p->flags
    );
}

void
dump_decoder(
    coderctx_t *d)
{
    elv_dbg("DECODER nb_streams=%d\n",
        d->format_context->nb_streams
    );
    for (int i = 0; i < d->format_context->nb_streams; i++) {
        AVStream *s = d->format_context->streams[i];
        elv_dbg("DECODER[%d] codec_type=%d start_time=%d duration=%d time_base=%d/%d frame_rate=%d/%d\n", i,
            s->codecpar->codec_type,
            (int)s->start_time, (int)s->duration,
            s->time_base.num, s->time_base.den,
            s->r_frame_rate.num, s->r_frame_rate.den
        );
    }

}

void
dump_encoder(
    AVFormatContext *format_context,
    txparams_t *params)
{
    if (!format_context)
        return;

    elv_dbg("ENCODER tx_type=%d, nb_streams=%d\n",
        params->tx_type,
        format_context->nb_streams);

    for (int i = 0; i < format_context->nb_streams; i++) {
        AVStream *s = format_context->streams[i];
        AVRational codec_time_base = av_stream_get_codec_timebase(s);
        elv_dbg("ENCODER[%d] stream_index=%d, id=%d, codec_type=%d start_time=%d duration=%d nb_frames=%d "
                "time_base=%d/%d codec_time_base=%d/%d frame_rate=%d/%d avg_frame_rate=%d/%d\n", i,
            s->index, s->id,
            s->codecpar->codec_type,
            (int)s->start_time, (int)s->duration, (int)s->nb_frames,
            s->time_base.num, s->time_base.den,
            codec_time_base.num, codec_time_base.den,
            s->r_frame_rate.num, s->r_frame_rate.den,
            s->avg_frame_rate.num, s->avg_frame_rate.den
        );
    }

}

void
dump_codec_context(
    AVCodecContext *cc)
{
    if (!cc)
        return;

    elv_dbg("CODEC CONTEXT codec type=%d id=%d "
        "time_base=%d/%d framerate=%d/%d tpf=%d delay=%d "
        "width=%d height=%d aspect_ratio=%d/%d coded_width=%d coded_height=%d gop=%d "
        "keyint_min=%d refs=%d "
        "frame_size=%d frame_number=%d"
        "\n",
        cc->codec_type, cc->codec_id,
        cc->time_base.num, cc->time_base.den, cc->framerate.num, cc->framerate.den, cc->ticks_per_frame, cc->delay,
        cc->width, cc->height, cc->sample_aspect_ratio.num, cc->sample_aspect_ratio.den,
        cc->coded_width, cc->coded_height, cc->gop_size,
        cc->keyint_min, cc->refs,
        cc->frame_size, cc->frame_number
    );
}

void
dump_codec_parameters(
    AVCodecParameters *cp)
{
    elv_dbg("CODEC PARAMETERS codec type=%d id=%d format=%d tag=%d "
        "bit_rate=%d "
        "width=%d height=%d frame_size=%d"
        "\n",
        cp->codec_type, cp->codec_id, cp->format, cp->codec_tag,
        (int)cp->bit_rate,
        cp->width, cp->height, cp->frame_size
    );
}

void
dump_stream(
    AVStream *s)
{
    if (!s)
        return;

    AVRational codec_time_base = av_stream_get_codec_timebase(s);

    elv_dbg("STREAM idx=%d id=%d "
        "time_base=%d/%d start_time=%d duration=%d nb_frames=%d "
        "codec_time_base=%d/%d "
        "r_frame_rate=%d/%d avg_frame_rate=%d/%d "
        "\n",
        s->index, s->id,
        s->time_base.num, s->time_base.den, (int)s->start_time, (int)s->duration, (int)s->nb_frames,
        codec_time_base.num, codec_time_base.den,
        s->r_frame_rate.num, s->r_frame_rate.den, s->avg_frame_rate.num, s->avg_frame_rate.den
    );
}

void
save_gray_frame(
    unsigned char *buf,
    int wrap,
    int xsize,
    int ysize,
    char *name,
    int number)
{
    char filename[1024];
    snprintf(filename, sizeof(filename), "%s-%d.pgm", name, number);

    FILE *f;
    int i;
    f = fopen(filename,"w");
    // writing the minimal required header for a pgm file format
    // portable graymap format -> https://en.wikipedia.org/wiki/Netpbm_format#PGM_example
    fprintf(f, "P5\n%d %d\n%d\n", xsize, ysize, 255);

    // writing line by line
    for (i = 0; i < ysize; i++)
        fwrite(buf + i * wrap, 1, xsize, f);
    fclose(f);
}

void
dump_trackers(
    AVFormatContext *encoder_format_context,
    AVFormatContext *decoder_format_context)
{
    if (!encoder_format_context || !decoder_format_context)
        return;

    static long t0 = 0;
    struct timeval t;
    long ti;
    AVIOContext *avioctx;
    ioctx_t *inctx;
    out_tracker_t *out_tracker = (out_tracker_t *) encoder_format_context->avpipe_opaque;

    gettimeofday(&t, NULL);
    ti = t.tv_sec * 1000 + t.tv_usec / 1000;
    if (t0 == 0)
        t0 = ti;

    avioctx = (AVIOContext *) decoder_format_context->pb;
    if (!avioctx)
        /* prepare_decoder can fail and pb becomes NULL */
        return;
    inctx = (ioctx_t *) avioctx->opaque;

    elv_dbg("CODERS t=%02d.%03d read_pos=%"PRId64" seg_index=%d seg_pos=%d\n",
        (int)(ti - t0) / 1000, (int)(ti - t0) % 1000, inctx->read_pos,
        out_tracker->seg_index, out_tracker->last_outctx ? out_tracker->last_outctx->written_bytes:0);
}

static void
ffmpeg_log_handler(void* ptr, int level, const char* fmt, va_list vl) {
    elv_log_level_t elv_level;
    switch (level) {
    case AV_LOG_QUIET:
        return;
    case AV_LOG_PANIC:
    case AV_LOG_FATAL:
    case AV_LOG_ERROR:
        elv_level = elv_log_error;
        break;
    case AV_LOG_WARNING:
        elv_level = elv_log_warning;
        break;
    case AV_LOG_INFO:
        elv_level = elv_log_log;
        break;
    case AV_LOG_DEBUG:
    case AV_LOG_VERBOSE:
    default:
        // TODO map to a trace level or something. AV_LOG_DEBUG is too verbose
        // elv_level = elv_log_debug;
        return;
    }
    elv_vlog(elv_level, "FF", fmt, vl);
}

void
connect_ffmpeg_log() {
    av_log_set_callback(ffmpeg_log_handler);
}
