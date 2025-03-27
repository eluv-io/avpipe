#
# rules.make
# contains all the common parts of the build system
#
# delete xcoder to build on Mac

BINDIR=bin
LIBDIR=lib
INCDIR=include

OSNAME := $(shell uname -s)
LDFLAGS := \
		-lavpipe \
		-lutils \
		$(shell pkg-config --libs libavfilter libavcodec libavformat libavdevice libswresample libavresample libswscale libavutil libpostproc xcoder srt)
CFLAGS := $(shell pkg-config --cflags libavfilter libavcodec libavformat libavdevice libswresample libavresample libswscale libavutil libpostproc xcoder srt)

ifeq ($(OSNAME), Darwin)
	LDFLAGS := ${LDFLAGS} \
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
	LDFLAGS := ${LDFLAGS} \
		-lOpenCL \
		-lva \
		-lva-drm \
		-lva-x11 \
		-lpthread \
		-lsrt
endif
