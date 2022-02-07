include ./rules.make

TOP_DIR ?= $(shell pwd)
SUBDIRS=utils libavpipe etx avcmd

SRCS=avpipe_handler.c
OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

.PHONY: all test clean

.DEFAULT_GOAL := dynamic

all install: copy_libs check-env
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	done

dynamic: copy_libs_all all

clean: lclean
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	done

copy_libs:
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@(if [ ! -d $(INCDIR) ]; then mkdir $(INCDIR); fi)
	cp ${FFMPEG_DIST}/lib/libav*.a ${LIBDIR}
	cp ${FFMPEG_DIST}/lib/libswresample.a ${LIBDIR}
	cp ${FFMPEG_DIST}/lib/libswscale.a ${LIBDIR}
	cp ${FFMPEG_DIST}/lib/libpostproc.a ${LIBDIR}
	cp -r ${FFMPEG_DIST}/include/* ${INCDIR}

copy_libs_all:
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@(if [ ! -d $(INCDIR) ]; then mkdir $(INCDIR); fi)
	cp ${FFMPEG_DIST}/lib/libav* ${LIBDIR}
	cp ${FFMPEG_DIST}/lib/libswresample* ${LIBDIR}
	cp ${FFMPEG_DIST}/lib/libswscale* ${LIBDIR}
	cp ${FFMPEG_DIST}/lib/libpostproc* ${LIBDIR}
	cp -r ${FFMPEG_DIST}/include/* ${INCDIR}

avpipe:
	CGO_CFLAGS="-I./include" CGO_LDFLAGS="-L${TOP_DIR}/lib -lavcodec -lavformat -lavfilter -lavpipe -lavdevice -lswresample -libavresample -lswscale -lavutil -lpostproc -lutils -lz -lm -ldl -lvdpau -lva -lX11 -lpthread" go build -v
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

clean_test:
	@rm -rf test_out avpipe-test*.log

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif

test:
	@cd ./media
	@(if ! [ -x `command -v gsutil` ]; then echo "gsutil could not be found, install gsutil"; exit 1; fi) || exit 1
	@gsutil -m cp 'gs://qluvio-test-assets/*' ./media
	@go test --timeout 10000s
