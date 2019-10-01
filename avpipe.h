#pragma once

#include "avpipe_xc.h"

int
tx(txparams_t *params, char *filename, int bypass_filtering, int debug_frame_level);

int
probe(char *filename, int seekable, txprobe_t **txprobe);

void
set_loggers();

int
version();
