/*
 * avpipe_codec_refs.h
 *
 * Codec reference-frame helpers.
 */

#ifndef AVPIPE_CODEC_REFS_H
#define AVPIPE_CODEC_REFS_H

#include <stdint.h>

#include <libavcodec/avcodec.h>

#include "avpipe_xc.h"

int
avpipe_h264_refs_from_extradata(
    const uint8_t *extradata,
    int extradata_size);

int
avpipe_source_h264_refs(
    coderctx_t *decoder_context,
    int index,
    int *sps_refs,
    int *codec_context_refs,
    const char **used);

void
avpipe_apply_h264_refs(
    AVCodecContext *encoder_codec_context,
    coderctx_t *decoder_context,
    int index,
    xcparams_t *params);

#endif /* AVPIPE_CODEC_REFS_H */
