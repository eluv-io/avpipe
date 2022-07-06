#pragma once

#include "avpipe_xc.h"

int32_t
xc_init(
    xcparams_t *params,
    int32_t *handle);

int
xc_run(
    int32_t handle);

int
xc_cancel(
    int32_t handle);

int
xc(
    xcparams_t *params);

int
mux(
    xcparams_t *params);

const char *
get_pix_fmt_name(
    int pix_fmt);

const char *
get_profile_name(
    int codec_id,
    int profile);

int
probe(
    xcparams_t *params,
    xcprobe_t **xcprobe,
    int *n_streams);

void
set_loggers();

int
version();
