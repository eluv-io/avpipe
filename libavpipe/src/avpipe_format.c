/*
 * avpipe_format.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libavutil/log.h>
#include <libavutil/pixdesc.h>

#include "avpipe_utils.h"
#include "avpipe_xc.h"
#include "elv_log.h"

#include <sys/time.h>

int
selected_decoded_audio(
    coderctx_t *decoder_context,
    int stream_index)
{
    if (decoder_context->n_audio <= 0)
        return -1;

    for (int i=0; i<decoder_context->n_audio; i++) {
        if (decoder_context->audio_stream_index[i] == stream_index)
            return i;
    }

    return -1;
}

int
packet_clone(
    AVPacket *src,
    AVPacket **dst
) {
    *dst = av_packet_alloc();
    if (!*dst) {
        return -1;
    }
    if (av_packet_ref(*dst, src) < 0) {
        av_packet_free(dst);
        return -1;
    }
    return 0;
}

