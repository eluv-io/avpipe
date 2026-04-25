/*
 * avpipe_codec.h
 *
 * Codec-specific helpers.
 */

#include <libavcodec/avcodec.h>
#include <libavutil/mastering_display_metadata.h>

#include "avpipe_xc.h"

/*
 * Parse x265 master-display string:
 * "G(g_x,g_y)B(b_x,b_y)R(r_x,r_y)WP(wp_x,wp_y)L(l_max,l_min)"
 * (usual chromaticities scaled by 50000, luminance by 10000)
 *
 * Returns eav_success and fills *out on success, or eav_param on parse failure
 */
int
parse_master_display(
    const char *s,
    AVMasteringDisplayMetadata *out);

/*
 * Format AVMasteringDisplayMetadata struct into x265 master-display string
 * Buffer size 128 bytes is sufficient.
 *
 * Returns eav_success on success, or eav_param if either pointer is NULL.
 */
int
format_master_display(
    char *buf,
    size_t buf_size,
    const AVMasteringDisplayMetadata *m);

/*
 * Parse "<MaxCLL>,<MaxFALL>" string.
 *
 * Returns eav_success and fills *out on success, or eav_param on parse failure
 */
int
parse_max_cll(
    const char *s,
    AVContentLightMetadata *out);

/*
 * Format AVContentLightMetadata struct into string "<MaxCLL>,<MaxFALL>"
 * Buffer size 32 bytes is sufficient
 *
 * Returns eav_success on success, or eav_param if either pointer is NULL.
 */
int
format_max_cll(
    char *buf,
    size_t buf_size,
    const AVContentLightMetadata *c);

/*
 * Parse 265 master-display string and attach as
 * AV_PKT_DATA_MASTERING_DISPLAY_METADATA on ctx->coded_side_data.
 * Instructs mp4 muxer to write the mdcv box
 *
 * Returns eav_success on success, eav_param if the string can't be parsed,
 * or eav_mem_alloc if side-data allocation fails.
 */
int
attach_master_display(
    AVCodecContext *ctx,
    const char *s);

/*
 * Parse "<MaxCLL>,<MaxFALL>" and attach as AV_PKT_DATA_CONTENT_LIGHT_LEVEL
 * on ctx->coded_side_data.
 * Instructs mp4 muxer to write the clli box
 */
int
attach_max_cll(
    AVCodecContext *ctx,
    const char *s);
