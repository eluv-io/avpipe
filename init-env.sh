#!/bin/bash
#
# Initialize Go and CGO build environment
#
# Arguments: <ffmpeg-dir>
#

if test $# -lt 2; then
    echo "Required arguments: <FFMPEG-DIR> <SRT-DIR>"
    return
fi

script_dir="$( cd "$( dirname ${BASH_SOURCE[0]} )" && pwd )"

# realpath to handle relative paths without subtle include path failures
command -v realpath && elvdev_dir=$(realpath "$1/..") || elvdev_dir="$1/.."
avpipe_dir=$script_dir

export FFMPEG_DIST=$elvdev_dir/FFmpeg/dist/
export SRT_DIST=$2

unset PKG_CONFIG_PATH
export PKG_CONFIG_PATH="${FFMPEG_DIST}/lib/pkgconfig:${SRT_DIST}:${PKG_CONFIG_PATH:-}"

echo elvdev_dir=$elvdev_dir
echo avpipe_dir=$avpipe_dir
echo FFMPEG_DIST=$FFMPEG_DIST
echo SRT_DIST=$SRT_DIST
echo PKG_CONFIG_PATH=$PKG_CONFIG_PATH
