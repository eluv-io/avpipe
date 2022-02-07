#!/bin/bash
#
# Initialize Go and CGO build environment
#
# Arguments: <ffmpeg-dir>
#

if test $# -lt 1; then
    echo "Required arguments: <FFMPEG-DIR>"
    return
fi

script_dir="$( cd "$( dirname ${BASH_SOURCE[0]} )" && pwd )"

# realpath to handle relative paths without subtle include path failures
command -v realpath && elvdev_dir=$(realpath "$1/..") || elvdev_dir="$1/.."
avpipe_dir=$script_dir

export FFMPEG_DIST=$elvdev_dir/FFmpeg/dist/
export GOPRIVATE="github.com/qluvio/*"

export PKG_CONFIG_PATH="${FFMPEG_DIST}/lib/pkgconfig:${PKG_CONFIG_PATH:-}"
export CGO_CFLAGS="${CGO_CFLAGS:-} -I${FFMPEG_DIST}/include -I$avpipe_dir/include"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -L${FFMPEG_DIST}/lib -L$avpipe_dir/lib \
-lavpipe -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale \
-lavutil -lpostproc -lutils -lm -ldl -lpthread"

echo elvdev_dir=$elvdev_dir
echo avpipe_dir=$avpipe_dir
echo FFMPEG_DIST=$FFMPEG_DIST
echo PKG_CONFIG_PATH=$PKG_CONFIG_PATH
echo CGO_CFLAGS=$CGO_CFLAGS
echo CGO_LDFLAGS=$CGO_LDFLAGS
