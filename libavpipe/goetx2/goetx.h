#pragma once

#include "avpipe_xc.h"

int
tx(txparams_t *params, char *filename);

typedef struct TxParams {
    int startTimeTs;  
    int durationTs;
    char *startSegmentStr;
    int videoBitrate;
    int audioBitrate;
    int sampleRate;                // Audio sampling rate
    char *crfStr;
    int segDurationTs;
    int segDurationFr;
    char *segDurationSecsStr;
    char *codec;
    int encHeight;
    int encWidth;
} TxParams;

typedef char* CharPtr;

typedef enum avpipe_handler_type_t {
    avpipe_in_opener,
    avpipe_in_reader,
    avpipe_in_writer,
    avpipe_in_seeker,
    avpipe_in_closer,
    avpipe_out_opener,
    avpipe_out_reader,
    avpipe_out_writer,
    avpipe_out_seeker,
    avpipe_out_closer
} avpipe_handler_type_t;
