TOP_DIR=`pwd`
CGO_CFLAGS="-I../include -I../../utils/include -I${ELV_TOOLCHAIN_DIST_PLATFORM}/include" CGO_LDFLAGS="-L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L${TOP_DIR}/../../utils/lib -L${TOP_DIR}/../lib -lavpipe -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -lpostproc -lutils -lm -ldl -lpthread" go build -v -x
