TOP_DIR ?= $(shell pwd)

SRCS=elv_xc_test.c
BINDIR=bin
LIBDIRS=-L$(TOP_DIR)/../lib
INCDIRS=-I$(TOP_DIR)/../include
FLAGS=-O0 -ggdb -Wall -fPIC


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

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

all: etx install

install:
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo Installing etx
	@cp $(BINDIR)/etx $(TOP_DIR)/../$(BINDIR)

etx: $(OBJS)
	@echo Making etx
	gcc $? ${LIBDIRS} ${LIBS} -o $(BINDIR)/etx

$(BINDIR)/%.o: %.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR)

check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

