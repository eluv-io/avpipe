/*
 * avpipe_format.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>

avp_live_proto_t
find_live_proto(
    ioctx_t *inctx
);

avp_container_t
find_live_container(
    coderctx_t *decoder_context
);

int
is_live_source(
    coderctx_t *ctx
);

int
is_live_source_udp(
    coderctx_t *ctx
);

int
is_live_source_custom_reader(
    coderctx_t *ctx
);

int
is_live_container_mpegts(
    coderctx_t *ctx
);

int
num_audio_output(
    coderctx_t *decoder_context,
    xcparams_t *params
);

int
selected_decoded_audio(
    coderctx_t *decoder_context,
    int stream_index
);

int
audio_output_stream_index(
    coderctx_t *decoder_context,
    xcparams_t* params,
    int audio_stream_index
);

int
get_channel_layout_for_encoder(
    int channel_layout
);

int
calc_timebase(
    xcparams_t *params,
    int is_video,
    int timebase
);

int
packet_clone(
    AVPacket *src,
    AVPacket **dst
);

void frame_rescale_time_base(
    AVFrame *frame,
    AVRational src_time_base,
    AVRational dst_time_base);