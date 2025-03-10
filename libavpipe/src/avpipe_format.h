/*
 * avpipe_format.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>
#include <libavutil/opt.h>
#include <libavutil/log.h>
#include <libavutil/pixdesc.h>


int
selected_decoded_audio(
    coderctx_t *decoder_context,
    int stream_index
);

int
packet_clone(
    AVPacket *src,
    AVPacket **dst
);