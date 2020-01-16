#!/bin/bash
set -Eeuo pipefail

if [ "$#" -ne 3 ]; then
    echo "Illegal number of parameters"
    echo "Usage generate-sample.sh fabric-base-dir assets-base-dir result-file-name"
else
    fabric_dir=$1
    BASE_TEST_ASSETS=$2
    outfile=$3
    set --  # unset args before we source
    source "$( dirname "${BASH_SOURCE[0]}" )/../init-env.sh" $fabric_dir
    ./bin/etx -seg-duration-ts 8631360 -tx-type video -seg-duration 720 -f ${BASE_TEST_ASSETS}/media/bbb-trailer/orig/trailer_video_0.mp4 -watermark-text michelle@eluv.io -watermark-color black -watermark-size 24 -watermark-xloc W/2 -watermark-yloc H*0.9
    cat ./O/O1/init-stream0.m4s ./O/O1/chunk-stream0-00001.mp4 > ./O/O1/$outfile
fi
