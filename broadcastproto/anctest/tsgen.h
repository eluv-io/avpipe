/*
 * tsgen writes an MPEG-TS test input with a SMPTE ST 2038 ancillary data stream next to a copied video stream.
 * Test support only: ffmpeg's CLI cannot create such a stream, since its raw data demuxer has no codec id.
 */
#ifndef ANCTEST_TSGEN_H
#define ANCTEST_TSGEN_H

#include <stdint.h>

typedef struct tsgen_t tsgen_t;

/* Opens src, picks its first video stream and opens dst as MPEG-TS with a copy of that stream plus an ST 2038
 * data stream. With unmarked set the data stream is written as private data without the VANC registration, as
 * an encoder with a vendor descriptor would, so a demuxer sees a generic private data stream (ffmpeg: bin_data).
 * Returns NULL on failure, with the reason on stderr. */
tsgen_t *tsgen_open(const char *src, const char *dst, int unmarked);

/* Copies the next video packet to the output. Returns 1 with the packet's pts and dts in 90 kHz units, 0 at the
 * end of the input, < 0 on error. */
int tsgen_next_video(tsgen_t *g, int64_t *pts, int64_t *dts);

/* Writes one ST 2038 PES packet (stream_id 0xBD) with the given payload at pts, in 90 kHz units. The output
 * holds the video packets until the data stream has packets, so these may all come after the video as long as
 * they come in pts order. Returns 0 or < 0. */
int tsgen_write_anc(tsgen_t *g, int64_t pts, const uint8_t *data, int size);

/* Writes the trailer, closes both files and frees g. Returns 0 or < 0. */
int tsgen_close(tsgen_t *g);

#endif
