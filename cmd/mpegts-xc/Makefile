include ../../rules.make
TOP_DIR ?= $(shell pwd)

all: mpegts-xc install

# All Go source files in the project outside this command's own directory.
GO_SRCS := $(shell find ../.. -name '*.go' -not -path '../../cmd/mpegts-xc/*')

mpegts-xc: $(wildcard *.go) $(GO_SRCS) $(TOP_DIR)/../../lib/libavpipe.a
	CGO_CFLAGS="-I${TOP_DIR}/../.. -I${TOP_DIR}/../../include -I${SRT_DIST} -I${SRT_DIST}/srtcore" CGO_LDFLAGS="-L${TOP_DIR}/../../lib ${LIBS}" go build -v -x

clean:
	@echo "Making clean mpegts-xc"
	@rm -rf mpegts-xc *.log

install: mpegts-xc
	@(if [ ! -d $(TOP_DIR)/../../$(BINDIR) ]; then mkdir $(TOP_DIR)/../../$(BINDIR); fi)
	@echo "Installing mpegts-xc"
	@cp mpegts-xc $(TOP_DIR)/../../$(BINDIR)

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif
