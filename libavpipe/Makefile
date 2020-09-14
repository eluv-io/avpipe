TOP_DIR ?= $(shell pwd)
SRCS=avpipe_xc.c \
    avpipe_utils.c \
    avpipe_filters.c \
    avpipe_io.c \
    avpipe_mux.c

BINDIR=bin
LIBDIR=lib
INCDIR=include

GIT_VERSION := "$(shell git describe --abbrev=4 --dirty --always --tags)"

OSNAME := $(shell uname -s)

INCDIRS=-I${ELV_TOOLCHAIN_DIST_PLATFORM}/include -I../utils/include -Iinclude

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

ifdef AVP_VERSION
VERSION := $(AVP_VERSION)
else
VERSION := $(GIT_VERSION)
endif

FLAGS=-O0 -ggdb -Wall -fPIC -DVERSION=\"$(VERSION)\"

all: avpipelib install

install:
	@echo "VERSION=" $(VERSION)
	@(if [ ! -d $(TOP_DIR)/../$(LIBDIR) ]; then mkdir $(TOP_DIR)/../$(LIBDIR); fi)
	@echo Installing avpipelib
	@cp $(LIBDIR)/* $(TOP_DIR)/../$(LIBDIR)
	@(if [ ! -d $(TOP_DIR)/../$(INCDIR) ]; then mkdir $(TOP_DIR)/../$(INCDIR); fi)
	@echo Installing includes
	@cp $(INCDIR)/* $(TOP_DIR)/../$(INCDIR)

avpipelib: $(OBJS)
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@echo Making avpipelib
	@ar cr $(LIBDIR)/libavpipe.a $?
	@ranlib $(LIBDIR)/libavpipe.a

$(BINDIR)/%.o: src/%.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR) $(LIBDIR)

tags:
	ctags -R . ../../elv-toolchain/FFmpeg/FFmpeg ../utils

check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

