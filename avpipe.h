#pragma once

#include "avpipe_xc.h"

int
tx(txparams_t *params, char *filename, int debug_frame_level, int64_t *last_input_pts);

int
probe(char *filename, int seekable, txprobe_t **txprobe);

void
set_loggers();

int
version();
