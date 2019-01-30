#
# rules.make
# contains all the common parts of the build system
#

BINDIR=bin
LIBDIR=lib
INCDIR=include

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

