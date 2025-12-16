/*
 * HDR10+ JSON to binary converter header
 */

#ifndef HDR10PLUS_JSON_H
#define HDR10PLUS_JSON_H

#include <libavutil/hdr_dynamic_metadata.h>
#include <libavutil/buffer.h>

/*
 * Parse HDR10+ JSON (single scene entry) and convert to binary T.35 SEI payload.
 *
 * @param json         Input JSON string containing one scene's HDR10+ metadata
 * @param out_metadata Output pointer to allocated AVDynamicHDRPlus struct (caller must free with av_free)
 * @param out_buf      Output pointer to AVBufferRef containing binary T.35 payload (caller must unref)
 * @return 0 on success, -1 on error
 */
int avpipe_hdr10plus_json_to_metadata(const char *json, AVDynamicHDRPlus **out_metadata, AVBufferRef **out_buf);

#endif /* HDR10PLUS_JSON_H */
