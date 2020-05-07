#!/bin/bash
#
# Initialize Go and CGO build environment
#
# Arguments: <content-fabric-top-dir>
#

if test $# -lt 1; then
    echo "Required arguments: <CONTENT-FABRIC-TOP-DIR>"
    return
fi

script_dir="$( cd "$( dirname ${BASH_SOURCE[0]} )" && pwd )"

# realpath to handle relative paths without subtle include path failures
command -v realpath && elvdev_dir=$(realpath "$1/..") || elvdev_dir="$1/.."
avpipe_dir=$script_dir

if [[ -z "${ELV_TOOLCHAIN_DIST_PLATFORM}" ]]; then
    OS="`uname`"
    case $OS in
    'Darwin')
        DARWIN=`ls $elvdev_dir/elv-toolchain/dist | grep darwin`
        export ELV_TOOLCHAIN_DIST_PLATFORM=$elvdev_dir/elv-toolchain/dist/${DARWIN}
        ;;
    'Linux')
        LINUX=`ls $elvdev_dir/elv-toolchain/dist | grep linux`
        export ELV_TOOLCHAIN_DIST_PLATFORM=$elvdev_dir/elv-toolchain/dist/${LINUX}
        ;;
    esac
fi

export PKG_CONFIG_PATH="${ELV_TOOLCHAIN_DIST_PLATFORM}/lib/pkgconfig:$PKG_CONFIG_PATH"
export CGO_CFLAGS="$CGO_CFLAGS -I${ELV_TOOLCHAIN_DIST_PLATFORM}/include -I$avpipe_dir/include"
export CGO_LDFLAGS="$CGO_LDFLAGS -L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L$avpipe_dir/lib \
-lavpipe -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale \
-lavutil -lpostproc -lutils -lm -ldl -lpthread"

echo elvdev_dir=$elvdev_dir
echo avpipe_dir=$avpipe_dir
echo ELV_TOOLCHAIN_DIST_PLATFORM=$ELV_TOOLCHAIN_DIST_PLATFORM
echo PKG_CONFIG_PATH=$PKG_CONFIG_PATH
echo CGO_CFLAGS=$CGO_CFLAGS
echo CGO_LDFLAGS=$CGO_LDFLAGS
