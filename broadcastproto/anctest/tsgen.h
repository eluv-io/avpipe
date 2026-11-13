/*
 * tsgen writes an MPEG-TS test input with a SMPTE ST 2038 ancillary data stream next to a copied video stream.
 * Test support only: ffmpeg's CLI cannot create such a stream, since its raw data demuxer has no codec id.
 */
#ifndef ANCTEST_TSGEN_H
#define ANCTEST_TSGEN_H

#include <stdint.h>

typedef struct tsgen_t tsgen_t;

/* Opens src, picks its first video stream and opens dst as MPEG-TS with a copy of the video and audio streams
 * plus an ST 2038 data stream. With unmarked set the data stream is written as private data without the VANC
 * registration, as an encoder with a vendor descriptor would, so a demuxer sees a generic private data stream
 * (ffmpeg: bin_data). With dst NULL nothing is written: the handle only scans src with tsgen_next_video.
 * Returns NULL on failure, with the reason on stderr. */
tsgen_t *tsgen_open(const char *src, const char *dst, int unmarked);

/* Copies the input's packets to the output up to and including the next video packet. Returns 1 with that
 * packet's pts and dts in 90 kHz units, 0 at the end of the input, < 0 on error. */
int tsgen_next_video(tsgen_t *g, int64_t *pts, int64_t *dts);

/* Writes one ST 2038 PES packet (stream_id 0xBD) with the given payload at pts, in 90 kHz units. Packets land
 * in the output where they are written, between the calls of tsgen_next_video, and must come in pts order.
 * Returns 0 or < 0. */
int tsgen_write_anc(tsgen_t *g, int64_t pts, const uint8_t *data, int size);

/* Writes the trailer if there is an output, closes the files and frees g. Returns 0 or < 0. */
int tsgen_close(tsgen_t *g);

#endif
