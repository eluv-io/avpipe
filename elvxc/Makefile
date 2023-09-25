include ../rules.make
TOP_DIR ?= $(shell pwd)

all: elvxc install

elvxc: main.go ../avpipe.go cmd/transcode.go
	CGO_CFLAGS="-I${TOP_DIR}/.. -I${TOP_DIR}/../include -I${SRT_DIST} -I${SRT_DIST}/srtcore" CGO_LDFLAGS="-L${TOP_DIR}/../lib ${LIBS}" go build -v -x

clean:
	@echo "Making clean elvxc"
	@rm -rf elvxc *.log

install: elvxc
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo "Installing elvxc"
	@cp elvxc $(TOP_DIR)/../$(BINDIR)
	
check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif

