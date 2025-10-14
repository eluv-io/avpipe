#!/bin/bash

export CGO_CFLAGS="-g -O0 -I/home/jan/ELV/avpipe -I/home/jan/ELV/avpipe/libavpipe/include -I/home/jan/ELV/avpipe/utils/include -I/home/jan/.local/include/srt -I/home/jan/.local/include"
export CGO_LDFLAGS="-L/home/jan/ELV/avpipe -lcbor"
