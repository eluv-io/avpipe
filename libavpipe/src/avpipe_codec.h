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
 * Parse x265 master-display string and attach as
 * AV_FRAME_DATA_MASTERING_DISPLAY_METADATA on ctx->decoded_side_data.
 * Applies to both libx265 and nvenc wrapper
 * Must be called before avcodec_open2().
 *
 * Returns eav_success on success, eav_param if the string can't be parsed,
 * or eav_mem_alloc if side-data allocation fails.
 */
int
attach_master_display(
    AVCodecContext *ctx,
    const char *s);

/*
 * Parse "<MaxCLL>,<MaxFALL>" and attach as AV_FRAME_DATA_CONTENT_LIGHT_LEVEL
 * on ctx->decoded_side_data.
 * Applies to both libx265 and nvenc wrapper
 * Must be called before avcodec_open2().
 */
int
attach_max_cll(
    AVCodecContext *ctx,
    const char *s);
