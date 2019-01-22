#
# Initialize Go and CGO build environment
#
# Argumantes: <content-fabric-top-dir>
#

if test $# -lt 1; then
    echo "Required arguments: <CONTENT-FABRIC-TOP-DIR>"
    return
fi

script_dir="$( cd "$( dirname ${BASH_SOURCE[0]} )" && pwd )"

elvdev_dir=$1/..
avpipe_dir=$script_dir
godev_dir="$( cd "$( dirname ${BASH_SOURCE[0]} )/../../../.." && pwd )"

if ! test -d $elvdev_dir/content-fabric; then
    echo "CONTENT-FABRIC-TOP-DIR doesn't contain directory 'content-fabric'"
    return
fi

export GOPATH=$godev_dir:$elvdev_dir/content-fabric
export ELV_TOOLCHAIN_DIST_PLATFORM=$elvdev_dir/elv-toolchain/dist/darwin-10.14

export CGO_CFLAGS="$CGO_CFLAGS -I${ELV_TOOLCHAIN_DIST_PLATFORM}/include -I$avpipe_dir/include"
export CGO_LDFLAGS="$CGO_LDFLAGS -L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L$avpipe_dir/lib \
-lavpipe -lutils  \
-lavpipe -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -lpostproc -lutils -lm -ldl -lpthread"
