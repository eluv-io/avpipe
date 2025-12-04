include ./rules.make

TOP_DIR ?= $(shell pwd)
SUBDIRS=utils libavpipe exc elvxc

SRCS=avpipe_handler.c
OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

.PHONY: all test clean

.DEFAULT_GOAL := dynamic

all install: check-env
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	done

dynamic: all

goclean: clean
	@go clean -modcache -cache -testcache -i -r

clean: lclean
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	done

avpipe:
	go build -v
	mkdir -p ./O

avpipe-debug: debug-libs
	@echo "Building avpipe with debug symbols..."
	go build -v -gcflags="all=-N -l" -o avpipe.debug
	mkdir -p ./O

debug-libs:
	@echo "Building C libraries with debug symbols..."
	$(MAKE) -C utils DEBUG=1
	$(MAKE) -C libavpipe DEBUG=1
	$(MAKE) -C exc DEBUG=1
	$(MAKE) -C elvxc DEBUG=1

libavpipego: $(OBJS)
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@echo Making libavpipe_handler
	@ld -r -o $(LIBDIR)/libavpipe_handler.a $?

$(BINDIR)/%.o: %.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${CFLAGS} ${LDFLAGS} ${INCDIRS} -c $< -o $@

lclean:
	@rm -rf lib bin include

clean_test:
	@rm -rf test_out avpipe-test*.log

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif

test:
	@./run_tests.sh

test-tcmalloc:
	@./run_tests_with_tcmalloc.sh

test-tcmalloc-strict:
	@HEAPCHECK=strict ./run_tests_with_tcmalloc.sh
