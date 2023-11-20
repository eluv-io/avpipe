/*
 * scte35.c
 */

#include "scte35.h"

/*
 * Parse SCTE-35 command type. Used to dispatch the SCTE-35 signal to a downstream processor.
 * Could be extended to parse more SCTE-35 information if necessary.
 *
 * References:
 *
 * - Carlos Fernandez Sanz PR "SCTE-35 support in hlsenc" (not accepted in ffmpeg)
 *   https://patchwork.ffmpeg.org/project/ffmpeg/patch/1476837390-20225-4-git-send-email-carlos@ccextractor.org/
 * - Github
 *   https://github.com/nixiz/scte35-parser
 *
 * Simplified byte format (up to the command type)
 *
 *   -  8 bits  - table identifier (must be 0xfc)
 *   -  1 bit   - section syntax indicator
 *   -  1 bit   - private indicator
 *   -  2 bits  - reserved
 *   - 12 bits  - section length
 *   -  8 bits  - protocol version
 *   -  1 bit   - encrypted packet
 *   -  6 bits  - encryption algorithm
 *   - 33 bits  - pts adjustment
 *   -  8 bits  - cw index
 *   - 12 bits  - tier
 *   - 12 bits  - splice command length
 *   -  8 bits  - command type
 *   (splice info follows)
 */
int parse_scte35_pkt(uint8_t *scte35_cmd_type, const AVPacket *avpkt)
{
    const uint8_t *buf = avpkt->data;

    if (scte35_cmd_type == NULL) {
        return -1;
    }

    *scte35_cmd_type = 0;

    if (avpkt->size < 20) {
        return -1; // Buffer too short
    }

    uint8_t table_identifier = buf[0];
    if (table_identifier != 0xfc) {
        return -1; // Unrecognized table identifier
    }

    *scte35_cmd_type = buf[13];
    return 0;
}
