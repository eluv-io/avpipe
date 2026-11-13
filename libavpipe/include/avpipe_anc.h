/*
 * Reads the SMPTE ST 2038 ancillary data stream of an input through libavformat, packet by packet, for tools that
 * want what the in_stat_data_anc stat reports without decoding or transcoding anything. The stream is picked as
 * prepare_decoder picks it: the smpte_2038 data stream, or the data stream whose id (TS PID) is anc_stream_id.
 */
#ifndef AVPIPE_ANC_H
#define AVPIPE_ANC_H

#include <libavformat/avformat.h>

#include "avpipe_xc.h"

typedef struct anc_scan_t {
    AVFormatContext *fc;
    AVPacket        *pkt;
    int             stream_index;
    int             stream_id;      /* TS PID */
    char            codec_name[32]; /* "smpte_2038", or what the demuxer made of a stream picked by id ("bin_data") */
    char            codec_tag[AV_FOURCC_MAX_STRING_SIZE];
} anc_scan_t;

/* Opens url and finds its ancillary data stream. Returns 0 and the scan, -1 when the input cannot be opened or
 * read, -2 when it has no such stream. */
int anc_scan_open(const char *url, int anc_stream_id, anc_scan_t **scan);

/* Reads the next packet of the stream. Returns 1 with pkt filled (data valid until the next call), 0 at the end
 * of the input, < 0 on a read error. */
int anc_scan_next(anc_scan_t *scan, anc_packet_t *pkt);

void anc_scan_close(anc_scan_t *scan);

#endif
