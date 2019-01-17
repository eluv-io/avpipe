SRCS=avpipe_xc.c avpipe_utils.c avpipe_filters.c avpipe_io.c
ETX_SRCS=elv_xc_test.c

AVLIBS=-lavpipe -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -lpostproc -lutils -lm -ldl -lpthread

BINDIR=bin
LIBDIR=lib

OSNAME := $(shell uname -s)

INCDIRS=-I${ELV_TOOLCHAIN_DIST_PLATFORM}/include -I../utils/include -Iinclude

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)
ETX_OBJS=$(ETX_SRCS:%.c=$(BINDIR)/%.o)

LIBDIRS=-L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L../utils/lib -L./lib
FLAGS=-O0 -ggdb -Wall -fPIC -DAUDIO

all: avpipelib etx goetx

avpipelib: $(OBJS)
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@echo Making avpipelib
	@ar cr $(LIBDIR)/libavpipe.a $?
	@ranlib $(LIBDIR)/libavpipe.a

etx: $(ETX_OBJS)
	@echo Making etx
	gcc $? ${LIBDIRS} ${AVLIBS} -o $(BINDIR)/etx

goetx: avpipelib
	@echo Making goetx
	@(cd goetx; make)

$(BINDIR)/%.o: src/%.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

$(BINDIR)/elv_xc_test.o: etx/elv_xc_test.c
	@echo "Compiling " $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR) $(LIBDIR)
	(cd goetx; make clean)

tags:
	ctags -R . ../../elv-toolchain/FFmpeg/FFmpeg ../utils

check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

