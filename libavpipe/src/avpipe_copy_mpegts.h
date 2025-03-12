#include "avpipe_xc.h"

int copy_mpegts_init(
    xctx_t *
);

void *copy_mpegts_func(
    void *p
);

int copy_mpegts_prepare_encoder(
    coderctx_t *encoder_context,
    coderctx_t *decoder_context,
    avpipe_io_handler_t *out_handlers,
    ioctx_t *inctx,
    xcparams_t *params
);
