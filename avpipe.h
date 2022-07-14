/*
 * avpipe.h
 *
 * Defines all the interfaces available to Go layer.
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
 *          The transcoding context is internal to C/Go layer.
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
 *
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
 */
int
xc_run(
    int32_t handle);

/**
 * @brief   Cancels or stops the transcoding specified by handle.
 *
 * @param   handle      The handle of transcoding context that is obtained by xc_init().
 *
 * @return  If it is successful it returns eav_success, otherwise eav_xc_table.
 */
int
xc_cancel(
    int32_t handle);

/**
 * @brief   Starts a transcoding job.
 *
 * @param   params      Transcoding parameters.
 *
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
 */
int
xc(
    xcparams_t *params);

/**
 * @brief   Starts a muxing job.
 *
 * @param   params      Muxing parameters.
 *
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
 */
int
mux(
    xcparams_t *params);

/**
 * @brief   Returns pixel format name.
 *
 * @param   pix_fmt     pixel format id.
 *
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
 *
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
 *
 * @return  If it is successful it returns eav_success, otherwise corresponding error.
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
