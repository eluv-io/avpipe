/*
 * Manual SMPTE ST 2094-40 (HDR10+) T.35 SEI payload encoder
 *
 * This is a workaround for FFmpeg 7.1's av_dynamic_hdr_plus_to_t35() FPE bug.
 * Implements basic ST 2094-40 encoding according to the specification.
 */

#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include "elv_log.h"

/*
 * Manually construct T.35 SEI payload for HDR10+ metadata.
 *
 * Format (simplified ST 2094-40):
 * - itu_t_t35_country_code: 1 byte (0xB5)
 * - itu_t_t35_terminal_provider_code: 2 bytes (0x003C for SMPTE)
 * - itu_t_t35_terminal_provider_oriented_code: 2 bytes (0x0001)
 * - application_identifier: 1 byte (4)
 * - application_version: 1 byte (1 for ST 2094-40)
 * - num_windows: 1 byte
 * - For each window: targeted_system_display_maximum_luminance, maxscl, etc.
 *
 * Returns allocated buffer and size. Caller must free with free().
 */
int avpipe_hdr10plus_manual_t35_encode(
    int num_windows,
    int tsdml,  /* targeted_system_display_maximum_luminance (nits) */
    int maxscl_r, int maxscl_g, int maxscl_b,  /* max scene luminance (0-100000) */
    int avg_rgb,  /* average maxRGB (0-100000) */
    int dist_count,
    const int *dist_percentages,  /* array of percentiles (0-100) */
    const int *dist_values,       /* array of values (0-100000) */
    uint8_t **out_data,
    size_t *out_size)
{
    if (!out_data || !out_size) {
        return -1;
    }

    /* Allocate buffer with enough space for maximum possible payload
     * Header: 8 bytes + window params + 15 distribution pairs max
     * Each distribution pair: ~3 bytes, total ~100 bytes max, use 512 for safety
     */
    size_t buf_size = 512;
    uint8_t *buf = malloc(buf_size);
    if (!buf) {
        elv_err("HDR10+ T.35: malloc failed");
        return -1;
    }

    size_t pos = 0;

    /* T.35 header */
    buf[pos++] = 0xB5;  /* itu_t_t35_country_code */
    buf[pos++] = 0x00;  /* itu_t_t35_terminal_provider_code (MSB) */
    buf[pos++] = 0x3C;  /* itu_t_t35_terminal_provider_code (LSB) - SMPTE */
    buf[pos++] = 0x00;  /* itu_t_t35_terminal_provider_oriented_code (MSB) */
    buf[pos++] = 0x01;  /* itu_t_t35_terminal_provider_oriented_code (LSB) */
    buf[pos++] = 0x04;  /* application_identifier (4 = ST 2094-40) */
    buf[pos++] = 0x01;  /* application_version (1) */

    /* Number of windows (bits: 2 bits, value 0-2 means 1-3 windows) */
    buf[pos++] = (num_windows - 1) & 0x03;

    /* Window 0 parameters (full frame) */
    /* Targeted system display maximum luminance (17 bits: 0-10000 * 10) */
    uint32_t tsdml_val = tsdml * 10;  /* Convert nits to 0.1 nit units */
    if (pos + 3 > buf_size) goto overflow;
    buf[pos++] = (tsdml_val >> 9) & 0xFF;
    buf[pos++] = (tsdml_val >> 1) & 0xFF;
    buf[pos] = (tsdml_val & 0x01) << 7;

    /* maxscl (3 x 17 bits each, in 0.00001 units) */
    for (int i = 0; i < 3; i++) {
        uint32_t maxscl_val = (i == 0) ? maxscl_r : (i == 1) ? maxscl_g : maxscl_b;
        /* Write 17 bits across bytes */
        if (pos + 3 > buf_size) goto overflow;
        buf[pos] |= (maxscl_val >> 10) & 0x7F;
        pos++;
        buf[pos++] = (maxscl_val >> 2) & 0xFF;
        buf[pos] = (maxscl_val & 0x03) << 6;
    }

    /* average_maxrgb (17 bits) */
    uint32_t avg_val = avg_rgb;
    if (pos + 3 > buf_size) goto overflow;
    buf[pos] |= (avg_val >> 11) & 0x3F;
    pos++;
    buf[pos++] = (avg_val >> 3) & 0xFF;
    buf[pos] = (avg_val & 0x07) << 5;

    /* num_distribution_maxrgb_percentiles (4 bits) */
    buf[pos] |= (dist_count & 0x0F) << 1;

    /* distribution_maxrgb (array of percentile + value pairs) */
    for (int i = 0; i < dist_count && i < 15; i++) {
        /* percentage (7 bits) */
        if (pos + 4 > buf_size) goto overflow;
        buf[pos] |= (dist_percentages[i] >> 6) & 0x01;
        pos++;
        buf[pos] = (dist_percentages[i] & 0x3F) << 2;

        /* percentile value (17 bits) */
        uint32_t pval = dist_values[i];
        buf[pos] |= (pval >> 15) & 0x03;
        pos++;
        buf[pos++] = (pval >> 7) & 0xFF;
        buf[pos] = (pval & 0x7F) << 1;
    }

    pos++;  /* Move to next byte boundary */

    *out_data = buf;
    *out_size = pos;

    elv_log("HDR10+ manual T.35 encode: %zu bytes", pos);
    return 0;

overflow:
    elv_err("HDR10+ T.35: buffer overflow at pos=%zu", pos);
    free(buf);
    *out_data = NULL;
    *out_size = 0;
    return -1;
}
