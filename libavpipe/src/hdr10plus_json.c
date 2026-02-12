/*
 * HDR10+ JSON to AVDynamicHDRPlus converter
 * Parses SMPTE ST 2094-40 JSON format and converts to binary SEI payload
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>
#include <libavutil/hdr_dynamic_metadata.h>
#include <libavutil/rational.h>
#include <libavutil/mem.h>
#include "elv_log.h"

/* Simple JSON value extractor - finds "key": value or "key": "value" */
static int json_get_int(const char *json, const char *key, int *out)
{
    char search[256];
    snprintf(search, sizeof(search), "\"%s\"", key);
    const char *p = strstr(json, search);
    if (!p) return -1;

    p = strchr(p + strlen(search), ':');
    if (!p) return -1;

    while (*p && isspace(*p)) p++;
    if (*p == ':') p++;
    while (*p && isspace(*p)) p++;

    *out = atoi(p);
    return 0;
}

/* Extract array of integers from JSON */
static int json_get_int_array(const char *json, const char *key, int *out, int max_count)
{
    char search[256];
    snprintf(search, sizeof(search), "\"%s\"", key);
    const char *p = strstr(json, search);
    if (!p) return -1;

    p = strchr(p + strlen(search), '[');
    if (!p) return -1;
    p++; /* skip '[' */

    int count = 0;
    while (*p && *p != ']' && count < max_count) {
        while (*p && (isspace(*p) || *p == ',')) p++;
        if (*p == ']') break;
        out[count++] = atoi(p);
        while (*p && *p != ',' && *p != ']') p++;
    }

    return count;
}

/*
 * Parse single scene JSON and populate AVDynamicHDRPlus struct.
 * Input JSON should be one scene entry with fields:
 *   NumberOfWindows, TargetedSystemDisplayMaximumLuminance,
 *   LuminanceParameters.MaxScl, LuminanceParameters.AverageRGB,
 *   LuminanceDistributions.DistributionIndex, LuminanceDistributions.DistributionValues
 */
int avpipe_hdr10plus_json_to_metadata(const char *json, AVDynamicHDRPlus **out_metadata, AVBufferRef **out_buf)
{
    if (!json || !out_metadata || !out_buf) {
        elv_err("HDR10+ JSON converter: NULL parameters");
        return -1;
    }

    /* Allocate AVDynamicHDRPlus structure */
    size_t alloc_size = 0;
    AVDynamicHDRPlus *hdr = av_dynamic_hdr_plus_alloc(&alloc_size);
    if (!hdr) {
        elv_err("Failed to allocate AVDynamicHDRPlus");
        return -1;
    }

    elv_dbg("HDR10+ allocated struct size=%zu, parsing JSON", alloc_size);

    /* Set required T.35 fields */
    hdr->itu_t_t35_country_code = 0xB5;  /* Required by SMPTE ST 2094-40 */
    hdr->application_version = 1;         /* ST 2094-40 specifies version 1 */

    /* Initialize all rationals to zero (safer than leaving uninitialized) */
    hdr->targeted_system_display_maximum_luminance = av_make_q(0, 1);

    /* Set required window coordinates for first processing window (full frame) */
    hdr->params[0].window_upper_left_corner_x = av_make_q(0, 1);
    hdr->params[0].window_upper_left_corner_y = av_make_q(0, 1);
    hdr->params[0].window_lower_right_corner_x = av_make_q(1, 1);
    hdr->params[0].window_lower_right_corner_y = av_make_q(1, 1);

    for (int i = 0; i < 3; i++) {
        hdr->params[0].maxscl[i] = av_make_q(0, 1);
    }
    hdr->params[0].average_maxrgb = av_make_q(0, 1);

    /* Parse basic fields */
    int num_windows = 1;
    json_get_int(json, "NumberOfWindows", &num_windows);
    hdr->num_windows = num_windows;
    elv_dbg("HDR10+ num_windows=%d", num_windows);

    int tsdml = 0;
    json_get_int(json, "TargetedSystemDisplayMaximumLuminance", &tsdml);
    elv_dbg("HDR10+ tsdml=%d, making rational", tsdml);
    hdr->targeted_system_display_maximum_luminance = av_make_q(tsdml, 1);
    elv_dbg("HDR10+ tsdml rational created");

    /* Parse MaxScl (R, G, B maxSCL) */
    int maxscl_values[3] = {0, 0, 0};
    json_get_int_array(json, "MaxScl", maxscl_values, 3);
    elv_dbg("HDR10+ maxscl=%d,%d,%d", maxscl_values[0], maxscl_values[1], maxscl_values[2]);
    for (int i = 0; i < 3; i++) {
        hdr->params[0].maxscl[i] = av_make_q(maxscl_values[i], 100000);
    }
    elv_dbg("HDR10+ maxscl rationals created");

    /* Parse AverageRGB */
    int avg_rgb = 0;
    json_get_int(json, "AverageRGB", &avg_rgb);
    elv_dbg("HDR10+ avg_rgb=%d", avg_rgb);
    hdr->params[0].average_maxrgb = av_make_q(avg_rgb, 100000);
    elv_dbg("HDR10+ avg_rgb rational created");

    /* Parse distribution percentiles */
    int dist_index[25] = {0};
    int dist_values[25] = {0};
    int dist_count_idx = json_get_int_array(json, "DistributionIndex", dist_index, 25);
    int dist_count_val = json_get_int_array(json, "DistributionValues", dist_values, 25);

    elv_dbg("HDR10+ dist_count_idx=%d, dist_count_val=%d", dist_count_idx, dist_count_val);

    if (dist_count_idx > 0 && dist_count_val > 0 && dist_count_idx == dist_count_val) {
        hdr->params[0].num_distribution_maxrgb_percentiles = dist_count_idx;
        for (int i = 0; i < dist_count_idx && i < 15; i++) {
            hdr->params[0].distribution_maxrgb[i].percentage = dist_index[i];
            hdr->params[0].distribution_maxrgb[i].percentile = av_make_q(dist_values[i], 100000);
        }
    }
    elv_dbg("HDR10+ distribution rationals created");

    /* Use manual T.35 encoder instead of FFmpeg's buggy av_dynamic_hdr_plus_to_t35() */
    extern int avpipe_hdr10plus_manual_t35_encode(
        int num_windows, int tsdml,
        int maxscl_r, int maxscl_g, int maxscl_b,
        int avg_rgb, int dist_count,
        const int *dist_percentages, const int *dist_values,
        uint8_t **out_data, size_t *out_size);

    uint8_t *raw_data = NULL;
    size_t raw_size = 0;

    int ret = avpipe_hdr10plus_manual_t35_encode(
        num_windows, tsdml,
        maxscl_values[0], maxscl_values[1], maxscl_values[2],
        avg_rgb, dist_count_idx,
        dist_index, dist_values,
        &raw_data, &raw_size);

    if (ret < 0 || !raw_data) {
        elv_err("Failed to manually encode HDR10+ to T.35: %d", ret);
        av_free(hdr);
        return -1;
    }

    /* Wrap in AVBufferRef */
    AVBufferRef *buf = av_buffer_create(raw_data, raw_size, av_buffer_default_free, NULL, 0);
    if (!buf) {
        elv_err("Failed to create AVBufferRef for HDR10+ T.35 data");
        free(raw_data);
        av_free(hdr);
        return -1;
    }

    elv_log("HDR10+ successfully encoded to T.35 binary, size=%zu", raw_size);

    *out_metadata = hdr;
    *out_buf = buf;
    return 0;
}

/*
 * Convert AVDynamicHDRPlus metadata to JSON string.
 * Returns newly allocated JSON string that caller must free(), or NULL on error.
 */
char *avpipe_hdr10plus_metadata_to_json(const AVDynamicHDRPlus *hdr)
{
    if (!hdr) {
        elv_err("HDR10+ metadata_to_json: NULL metadata");
        return NULL;
    }

    // Allocate buffer for JSON output (4KB should be plenty)
    size_t buf_size = 4096;
    char *json = (char *)malloc(buf_size);
    if (!json) {
        elv_err("HDR10+ metadata_to_json: malloc failed");
        return NULL;
    }

    int pos = 0;

    // Start JSON object - use field names that match the parser expectations
    pos += snprintf(json + pos, buf_size - pos, "{");

    // NumberOfWindows
    pos += snprintf(json + pos, buf_size - pos,
                   "\"NumberOfWindows\":%d,", hdr->num_windows);

    // TargetedSystemDisplayMaximumLuminance
    int tsdml = hdr->targeted_system_display_maximum_luminance.num /
                (hdr->targeted_system_display_maximum_luminance.den ?
                 hdr->targeted_system_display_maximum_luminance.den : 1);
    pos += snprintf(json + pos, buf_size - pos,
                   "\"TargetedSystemDisplayMaximumLuminance\":%d,", tsdml);

    // MaxScl values (R, G, B) - single array at top level
    pos += snprintf(json + pos, buf_size - pos, "\"MaxScl\":[");
    for (int i = 0; i < 3; i++) {
        int val = (hdr->params[0].maxscl[i].num * 100000) /
                  (hdr->params[0].maxscl[i].den ? hdr->params[0].maxscl[i].den : 1);
        pos += snprintf(json + pos, buf_size - pos, "%d%s", val, (i < 2 ? "," : ""));
    }
    pos += snprintf(json + pos, buf_size - pos, "],");

    // AverageRGB
    int avg_rgb = (hdr->params[0].average_maxrgb.num * 100000) /
                  (hdr->params[0].average_maxrgb.den ? hdr->params[0].average_maxrgb.den : 1);
    pos += snprintf(json + pos, buf_size - pos, "\"AverageRGB\":%d", avg_rgb);

    // Distribution percentiles (if present)
    int num_dist = hdr->params[0].num_distribution_maxrgb_percentiles;
    if (num_dist > 0) {
        pos += snprintf(json + pos, buf_size - pos, ",\"DistributionIndex\":[");
        for (int i = 0; i < num_dist && i < 15; i++) {
            pos += snprintf(json + pos, buf_size - pos, "%d%s",
                           hdr->params[0].distribution_maxrgb[i].percentage,
                           (i < num_dist - 1 ? "," : ""));
        }
        pos += snprintf(json + pos, buf_size - pos, "],");

        pos += snprintf(json + pos, buf_size - pos, "\"DistributionValues\":[");
        for (int i = 0; i < num_dist && i < 15; i++) {
            int val = (hdr->params[0].distribution_maxrgb[i].percentile.num * 100000) /
                      (hdr->params[0].distribution_maxrgb[i].percentile.den ?
                       hdr->params[0].distribution_maxrgb[i].percentile.den : 1);
            pos += snprintf(json + pos, buf_size - pos, "%d%s", val,
                           (i < num_dist - 1 ? "," : ""));
        }
        pos += snprintf(json + pos, buf_size - pos, "]");
    }

    // Close main object
    pos += snprintf(json + pos, buf_size - pos, "}");

    elv_dbg("HDR10+ metadata_to_json: generated %d bytes of JSON", pos);

    return json;
}
