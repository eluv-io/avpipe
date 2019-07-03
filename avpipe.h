#pragma once

#include "avpipe_xc.h"

int
tx(txparams_t *params, char *filename, int bypass_filtering, int debug_frame_level);

void
set_loggers();

int
version();
