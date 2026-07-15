include ./rules.make

TOP_DIR ?= $(shell pwd)
SUBDIRS=utils libavpipe exc elvxc cmd/mvhevc cmd/fmp4-validate

SRCS=avpipe_handler.c
OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

.PHONY: all test gotest ctest clean

.DEFAULT_GOAL := dynamic

all install: check-env
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	if [ "$$dir" = "libavpipe" ]; then \
		openssl dgst -md5 lib/libavpipe.a | awk '{print $$NF}' > lib/libavpipe.hash; \
	fi; \
	done

dynamic: all

# goclean: nuclear option to reset all caches. Normally not needed — `make`
# updates lib/libavpipe.hash which triggers automatic Go rebuild via //go:embed.
goclean: clean
	@go clean -cache -testcache -modcache -i -r

clean: lclean
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	done

avpipe:
	go build -v
	mkdir -p ./O

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

test: ctest gotest

gotest:
	@./run_tests.sh

ctest:
	@$(MAKE) -C libavpipe test
