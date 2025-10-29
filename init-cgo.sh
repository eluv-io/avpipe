#!/bin/bash

export CGO_CFLAGS="-g -O0 -I'$HOME/ELV/avpipe' -I$HOME/ELV/avpipe/libavpipe/include -I$HOME/ELV/avpipe/utils/include -I$HOME/.local/include/srt -I$HOME/.local/include"
export CGO_LDFLAGS="-L$HOME/ELV/avpipe -lcbor"
