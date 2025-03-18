#include "avpipe_xc.h"

void *copy_mpegts_func(
    void *p
);

int copy_mpegts_prepare_encoder(
    cp_ctx_t *cp_ctx,
    coderctx_t *decoder_context,
    avpipe_io_handler_t *out_handlers,
    ioctx_t *inctx,
    xcparams_t *params
);
