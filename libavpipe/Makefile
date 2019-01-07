SRCS=elv_xc_test.c elv_xc_utils.c elv_filters.c io.c

AVLIBS=-lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -lpostproc -lutils -lm -ldl -lpthread

BINDIR=bin
LIBDIR=lib

OSNAME := $(shell uname -s)

INCDIRS=-I${ELV_TOOLCHAIN_DIST_PLATFORM}/include -I../utils/include -Iinclude

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

LIBDIRS=-L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L../utils/lib
FLAGS=-O0 -ggdb -Wall -fPIC -DAUDIO

all: etx

etx: $(OBJS)
	@echo Making etx
	gcc $? ${LIBDIRS} ${AVLIBS} -o $(BINDIR)/etx

$(BINDIR)/%.o: src/%.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling ..." $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR) $(LIBDIR)

tags:
	ctags -R . ../../elv-toolchain/FFmpeg/FFmpeg ../utils

check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

