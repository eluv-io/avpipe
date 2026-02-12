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

/*
 * Convert AVDynamicHDRPlus metadata to JSON string.
 *
 * @param hdr          Input AVDynamicHDRPlus metadata structure
 * @return Newly allocated JSON string (caller must free()), or NULL on error
 */
char *avpipe_hdr10plus_metadata_to_json(const AVDynamicHDRPlus *hdr);

/*
 * Extract HDR10+ metadata from a video file and return as JSON string.
 * Decodes frames until HDR10+ dynamic metadata is found.
 *
 * @param url          Input video file path or URL
 * @param out_json     Output pointer to newly allocated JSON string (caller must free())
 * @param out_json_len Output pointer to JSON string length
 * @return 0 on success, -1 on error (no HDR10+ found or decode error)
 */
int avpipe_extract_hdr10plus_json(const char *url, char **out_json, int *out_json_len);

#endif /* HDR10PLUS_JSON_H */
