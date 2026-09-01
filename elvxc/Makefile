include ../rules.make
include ../uniqfeed.make
TOP_DIR ?= $(shell pwd)

all: elvxc install

# All Go source files in the project outside elvxc/ itself (e.g. avpipe.go,
# goavpipe/, mp4e/). elvxc/ is excluded to avoid a circular dependency on the
# output binary. main.go and cmd/*.go are tracked separately below.
GO_SRCS := $(shell find .. -name '*.go' -not -path '../elvxc/*')

elvxc: main.go $(wildcard cmd/*.go) $(GO_SRCS) $(TOP_DIR)/../lib/libavpipe.a
	CGO_CFLAGS="-I${TOP_DIR}/.. -I${TOP_DIR}/../include -I${SRT_DIST} -I${SRT_DIST}/srtcore" \
	CGO_LDFLAGS="-Wl,--copy-dt-needed-entries -Wl,--no-as-needed -L${TOP_DIR}/../lib ${UNIQFEED_LIBDIRS} ${LIBS} -l:libcommon_colorconv2_lib.so -l:libcommon_rpc_lib.so -l:libcommon_blob_store_lib.so -l:libcommon_cvtools_lib.so -l:libcommon_system_lib.so -l:libcommon.so -l:libcommon_arguments_lib.so -l:libcommon_allocator_lib.so -l:libcommon_definitions_lib.so -l:libcommon_image_lib.so -l:libcommon_logger3_lib.so -l:libcommon_media_store_lib.so -l:libcommon_contour_lib.so -l:libtracking_types.so -l:libcamoverlay_common.so -l:libball_tracking_common.so -l:libuidata.so -l:libstateservice_core.so -l:libmodel.so -l:libfdio.so -l:librender.so -l:libmedia.so -l:libmodel3d.so -l:libcamoverlay_renderer.so -l:libvisapp.so -l:libufvulkan.so -l:libdozer.so -l:libkeypoint.so.22.0.1 -l:libuf_common_rpc_proto.so -l:libstateservice.so -l:libuf_model_proto.so -l:libuf_api.so -l:libopencv_core.so.412 -l:libopencv_imgcodecs.so.412 -l:libopencv_imgproc.so.412 -l:libopencv_videoio.so.412 -l:libopencv_dnn.so.412 -l:libvulkan.so.1 -l:libnvinfer.so.10 -l:libnvinfer_plugin.so.10 -l:libnvonnxparser.so.10 -l:libredis++.so.1 $(UNIQFEED_AUTO_LIBS)" \
	go build -v -x

clean:
	@echo "Making clean elvxc"
	@rm -rf elvxc *.log

install: elvxc
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo "Installing elvxc"
	@cp elvxc $(TOP_DIR)/../$(BINDIR)

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif
