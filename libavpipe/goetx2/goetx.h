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
