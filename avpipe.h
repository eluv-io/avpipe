#pragma once

#include "avpipe_xc.h"

int32_t
tx_init(
    txparams_t *params,
    char *filename,
    int debug_frame_level,
    int32_t *handle);

int
tx_run(
    int32_t handle);

int
tx_cancel(
    int32_t handle);

int
tx(
    txparams_t *params,
    char *filename,
    int debug_frame_level);

int
mux(
    txparams_t *params,
    char *filename,
    int debug_frame_level);

const char *
get_pix_fmt_name(
    int pix_fmt);

const char *
get_profile_name(
    int codec_id,
    int profile);

int
probe(
    char *filename,
    int seekable,
    txprobe_t **txprobe,
    int *n_streams);

void
set_loggers();

int
version();
