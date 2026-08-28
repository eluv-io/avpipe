include ../rules.make
include ../uniqfeed.make
TOP_DIR ?= $(shell pwd)

SRCS=elv_xc.c elv_mux.c
LIBDIRS=-L$(TOP_DIR)/../lib -L/usr/local/lib
UNIQFEED_LIBS=-l:libcommon_colorconv2_lib.so -l:libcommon_rpc_lib.so -l:libcommon_blob_store_lib.so -l:libcommon_cvtools_lib.so -l:libcommon_system_lib.so -l:libcommon.so -l:libcommon_arguments_lib.so -l:libcommon_allocator_lib.so -l:libcommon_definitions_lib.so -l:libcommon_image_lib.so -l:libcommon_logger3_lib.so -l:libcommon_media_store_lib.so -l:libcommon_contour_lib.so -l:libtracking_types.so -l:libcamoverlay_common.so -l:libball_tracking_common.so -l:libuidata.so -l:libstateservice_core.so -l:libmodel.so -l:libfdio.so -l:librender.so -l:libmedia.so -l:libmodel3d.so -l:libcamoverlay_renderer.so -l:libvisapp.so -l:libufvulkan.so -l:libdozer.so -l:libkeypoint.so.22.0.1 -l:libuf_common_rpc_proto.so -l:libstateservice.so -l:libuf_model_proto.so -l:libuf_api.so -l:libopencv_core.so.412 -l:libopencv_imgcodecs.so.412 -l:libopencv_imgproc.so.412 -l:libopencv_videoio.so.412 -l:libopencv_dnn.so.412 -l:libvulkan.so.1 -l:libnvinfer.so.10 -l:libnvinfer_plugin.so.10 -l:libnvonnxparser.so.10 -l:libredis++.so.1
INCDIRS=-I$(TOP_DIR)/../include
FLAGS=-O0 -ggdb -Wall -fPIC

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

all: install

install: exc
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo Installing exc
	@cp $(BINDIR)/exc $(TOP_DIR)/../$(BINDIR)

exc: $(OBJS)
	@echo Making exc
	gcc $? ${CFLAGS} ${LDFLAGS} -Wl,--copy-dt-needed-entries -Wl,--no-as-needed ${UNIQFEED_RPATHLINKS} ${LIBDIRS} ${UNIQFEED_LIBDIRS} ${LIBS} ${UNIQFEED_LIBS} $(UNIQFEED_AUTO_LIBS) -o $(BINDIR)/exc

$(BINDIR)/%.o: %.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${CFLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR)

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif
