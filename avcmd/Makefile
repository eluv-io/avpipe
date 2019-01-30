include ../rules.make
TOP_DIR ?= $(shell pwd)

all: avcmd install

avcmd:
	GCO_CFLAGS="-I${TOP_DIR}/.. -I${TOP_DIR}/../include" CGO_LDFLAGS="-L${TOP_DIR}/../lib ${LIBS}" go build -v -x

clean:
	@echo "Making clean avcmd"
	@rm -rf avcmd *.log

install: avcmd
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo "Installing avcmd"
	@cp avcmd $(TOP_DIR)/../$(BINDIR)
	
check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

