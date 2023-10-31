#
# rules.make
# contains all the common parts of the build system
#

BINDIR=bin
LIBDIR=lib
INCDIR=include

OSNAME := $(shell uname -s)
LDFLAGS := $(shell pkg-config --libs libavfilter libavcodec libavformat libavdevice libswresample libavresample libswscale libavutil libpostproc) \
		-lavpipe \
		-lutils \
		-lm \
		-ldl \
		-lpthread
CFLAGS := $(shell pkg-config --cflags libavfilter libavcodec libavformat libavdevice libswresample libavresample libswscale libavutil libpostproc)

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
		-lpthread
endif

