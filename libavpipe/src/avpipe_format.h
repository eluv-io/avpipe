/*
 * avpipe_format.c
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>

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