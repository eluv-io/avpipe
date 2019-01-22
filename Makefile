TOP_DIR ?= $(shell pwd)
SUBDIRS=utils libavpipe avcmd

BINDIR=bin
LIBDIR=lib
SRCS=avpipe_handler.c
OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

INCDIRS=-I${ELV_TOOLCHAIN_DIST_PLATFORM}/include -I${TOP_DIR}/utils/include -I${TOP_DIR}/libavpipe/include

all clean install: check-env lclean
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	done

avpipe:
	CGO_CFLAGS="-I./libavpipe/include -I./utils/include -I${ELV_TOOLCHAIN_DIST_PLATFORM}/include" CGO_LDFLAGS="-L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L${TOP_DIR}/utils/lib -L${TOP_DIR}/libavpipe/lib -lavpipe -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -lpostproc -lutils -lm -ldl -lpthread" go build -v
	#CGO_CFLAGS="-I./libavpipe/include -I./utils/include -I${ELV_TOOLCHAIN_DIST_PLATFORM}/include" CGO_LDFLAGS="-L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib -L${TOP_DIR}/utils/lib -L${TOP_DIR}/libavpipe/lib -lavcodec -lavformat -lavfilter -lavdevice -lswresample -lswscale -lavutil -lpostproc -lm -ldl -lpthread" go build -v
	mkdir -p ./O

libavpipego: $(OBJS)
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@echo Making libavpipe_handler
	@ld -r -o $(LIBDIR)/libavpipe_handler.a $?

$(BINDIR)/%.o: %.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

lclean:
	@rm -rf lib bin include

check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

