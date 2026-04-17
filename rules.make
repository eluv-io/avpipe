#
# rules.make
# contains all the common parts of the build system
#

BINDIR=bin
LIBDIR=lib
INCDIR=include

# Debug build support
DEBUG ?= 0
ifeq ($(DEBUG), 1)
	CFLAGS_DEBUG := -g -O0 -DDEBUG
	LDFLAGS_DEBUG := -g
else
	CFLAGS_DEBUG :=
	LDFLAGS_DEBUG :=
endif

OSNAME := $(shell uname -s)
LDFLAGS := \
		-lavpipe \
		-lutils \
		$(shell pkg-config --libs libavfilter libavcodec libavformat libavdevice libswresample libavresample libswscale libavutil libpostproc srt) \
		$(LDFLAGS_DEBUG)
CFLAGS := $(shell pkg-config --cflags libavfilter libavcodec libavformat libavdevice libswresample libavresample libswscale libavutil libpostproc srt) \
		$(CFLAGS_DEBUG)

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
		$(shell pkg-config --libs xcoder)
	CFLAGS := ${CFLAGS} $(shell pkg-config --cflags xcoder)
endif
