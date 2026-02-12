/*
 * avpipe.h
 *
 * Defines all the interfaces available to the Go layer.
 * There are two sets of API's available for transcoding/probing in this layer:
 * - APIs with handle: these APIs allow the client application to cancel a transcoding if it is necessary.
 *   - xc_init(): to initialize a transcoding and obtain a handle.
 *   - xc_run(): to start a transcoding with obtained handle.
 *   - xc_cancel(): to cancel/stop a transcoding with specified handle.
 * - APIs with no handle: these APIs are very simple to use and just need transcoding/probing params.
 *   - xc(): starts a transcoding with specified transcoding params.
 *   - mux(): starts a muxing job with specified params.
 *   - probe(): probs the specified stream/file.
 *
 * Other miscellaneous APIs are:
 *   - get_pix_fmt_name(): to obtain pixel format name.
 *   - get_profile_name(): to obtain profile name.
 */
#pragma once

#include "avpipe_xc.h"

/**
 * @brief   Initializes a transcoding context and returns its handle.
 *          The transcoding context is internal to the C/Go layer.
 *
 * @param   params          Transcoding parameters.
 * @param   handle          Pointer to the handle of transcoding context.
 *
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
 */
int32_t
xc_init(
    xcparams_t *params,
    int32_t *handle);

/**
 * @brief   Starts the transcoding specified by handle.
 *
 * @param   handle      The handle of transcoding context that is obtained by xc_init().
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
 */
int
xc_run(
    int32_t handle);

/**
 * @brief   Cancels or stops the transcoding specified by handle.
 *
 * @param   handle      The handle of transcoding context that is obtained by xc_init().
 * @return  If it is successful it returns eav_success, otherwise eav_xc_table.
 */
int
xc_cancel(
    int32_t handle);

/**
 * @brief   Starts a transcoding job.
 *
 * @param   params      Transcoding parameters.
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
 */
int
xc(
    xcparams_t *params);

/**
 * @brief   Starts a muxing job.
 *
 * @param   params      Muxing parameters.
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
 */
int
mux(
    xcparams_t *params);

/**
 * @brief   Returns pixel format name.
 *
 * @param   pix_fmt     pixel format id.
 * @return  Returns pixel format name.
 */
const char *
get_pix_fmt_name(
    int pix_fmt);

/**
 * @brief   Returns profile name.
 *
 * @param   codec_id    codec id.
 * @param   profile     profile id.
 * @return  Returns profile name.
 */
const char *
get_profile_name(
    int codec_id,
    int profile);

/**
 * @brief   Starts a probing job.
 *
 * @param   params      Probing parameters.
 * @param   xcprobe     Probing information array, will be allocated inside this API.
 * @param   n_streams   Number of entries/streams in probing information array.
 * @return  If it is successful it returns eav_success and fills xcprobe array and n_streams,
 *          otherwise returns corresponding error.
 */
int
probe(
    xcparams_t *params,
    xcprobe_t **xcprobe,
    int *n_streams);

/**
 * @brief   Sets the Go loggers.
 *
 * @return  void.
 */
void
set_loggers();

/*
 * HDR10+ dynamic metadata API (simple global store keyed by frame PTS).
 * These helpers allow external producers to push JSON metadata for a frame
 * which the encoder pipeline can retrieve when processing that PTS.
 *
 * - `avpipe_set_hdr10plus`: copy JSON into internal store for given PTS.
 * - `avpipe_get_hdr10plus`: retrieve and remove JSON for given PTS; caller must free.
 */
int
avpipe_set_hdr10plus(
    int64_t pts,
    const char *json,
    int json_len);

char *
avpipe_get_hdr10plus(
    int64_t pts);

/* Clear all HDR10+ metadata from the store */
void
avpipe_clear_hdr10plus_store(void);

/* Configuration helpers for HDR10+ store */
int avpipe_hdr10plus_set_tolerance(int64_t tolerance_pts);
int avpipe_hdr10plus_set_ttl(int ttl_seconds);
int avpipe_hdr10plus_set_capacity(int max_entries);

/*
 * Extract HDR10+ dynamic metadata from a video file.
 * Decodes frames until HDR10+ metadata is found and returns as JSON string.
 *
 * - `avpipe_extract_hdr10plus_json`: extract HDR10+ from source file to JSON.
 */
int
avpipe_extract_hdr10plus_json(
    const char *url,
    char **out_json,
    int *out_json_len);

/*
 * Export all stored HDR10+ metadata to a JSON file in x265 format.
 * Writes an array of per-frame metadata objects sorted by PTS.
 * Returns 0 on success, -1 on error.
 *
 * - `avpipe_export_hdr10plus_to_file`: export all HDR10+ metadata to file for x265.
 */
int
avpipe_export_hdr10plus_to_file(
    const char *filepath);
