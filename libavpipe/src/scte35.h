/*
 * scte35.h
 */

#include <libavcodec/avcodec.h>
#include <libavformat/avformat.h>

#include <stdio.h>
#include <sys/types.h>

int
parse_scte35_pkt(
    uint8_t *scte35_cmd_type,
    const AVPacket *avpkt);
