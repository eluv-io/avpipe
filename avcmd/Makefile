TOP_DIR ?= $(shell pwd)

all: avpipe

avpipe:
	CGO_CFLAGS="-I${TOP_DIR}/.. -I${TOP_DIR}/../libavpipe/include -I${TOP_DIR}/../utils/include -I${ELV_TOOLCHAIN_DIST_PLATFORM}/include" CGO_LDFLAGS="-L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L${TOP_DIR}/../utils/lib -L${TOP_DIR}/../libavpipe/lib -L${TOP_DIR}/../lib -lavpipe -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -lpostproc -lutils -lm -ldl -lpthread" go build -v -x

clean:
	@echo "Making clean avcmd"
	@rm -rf avcmd *.log
	
check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

