TOP_DIR ?= $(shell pwd)

BINDIR=bin
OSNAME := $(shell uname -s)

ifeq ($(OSNAME), Darwin)
	LIBS=-lavpipe \
		-lavformat \
		-lavfilter \
		-lavcodec \
		-lavdevice \
		-lswresample \
		-lswscale \
		-lavutil \
		-lpostproc \
		-lutils \
		-lz \
		-lbz2 \
		-lm \
		-ldl \
		-lmp3lame \
		-lx264 \
		-lxvidcore \
		-lfontconfig \
		-lfribidi \
		-lfreetype \
		-liconv \
		-lpthread \
		-framework OpenGL \
		-framework CoreAudio \
		-framework AudioToolbox \
		-framework AudioUnit \
		-framework Carbon \
		-framework CoreMedia \
		-framework CoreVideo \
		-framework Foundation \
		-framework Security \
		-framework VideoToolbox \
		-framework OpenCL \
		-framework CoreImage \
		-framework AppKit
endif
ifeq ($(OSNAME), Linux)
	LIBS=-lavpipe \
		-lavformat \
		-lavfilter \
		-lavcodec \
		-lavdevice \
		-lswresample \
		-lswscale \
		-lavutil \
		-lpostproc \
		-lutils \
		-lz \
		-lm \
		-ldl \
		-lvdpau \
		-lX11 \
		-lmp3lame \
		-lx264 \
		-lxvidcore \
		-lOpenCL \
		-lfontconfig \
		-lfribidi \
		-lfreetype \
		-lva \
		-lva-drm \
		-lva-x11 \
		-lpthread
endif


all: avcmd

avcmd:
	GCO_CFLAGS="-I${TOP_DIR}/.. -I${TOP_DIR}/../include" CGO_LDFLAGS="-L${TOP_DIR}/../lib ${LIBS}" go build -v -x

clean:
	@echo "Making clean avcmd"
	@rm -rf avcmd *.log

install: avcmd
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo "Installing avcmd"
	@cp avcmd $(TOP_DIR)/../$(BINDIR)
	
check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

