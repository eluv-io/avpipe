# Makefile for fmp4-validate
#
# Paths assume:
#   - FFmpeg development packages are visible to pkg-config
#
# Build: make
# Clean: make clean

TOP_DIR ?= $(shell pwd)
ROOT_DIR ?= $(TOP_DIR)/../..
BINDIR ?= bin
UNAME_S := $(shell uname -s)

GO_TARGET    = fmp4-validate
GO_SRCS      = $(shell find . -name '*.go')
GO_BUILD_DIR = .build
GO_BINARY    = $(GO_BUILD_DIR)/$(GO_TARGET)
BINARIES     = $(GO_BINARY)
INSTALL_TARGETS = install-common

.PHONY: all install clean

all: $(BINARIES) install

$(GO_BINARY): $(GO_SRCS) ../../go.mod ../../go.sum
	@(if [ ! -d $(GO_BUILD_DIR) ]; then mkdir $(GO_BUILD_DIR); fi)
	go build -v -x -o $(GO_BINARY) .

install: $(INSTALL_TARGETS)

install-common: $(GO_BINARY)
	@(if [ ! -d $(ROOT_DIR)/$(BINDIR) ]; then mkdir $(ROOT_DIR)/$(BINDIR); fi)
	@echo "Installing $(GO_TARGET)"
	@cp $(GO_BINARY) $(ROOT_DIR)/$(BINDIR)/$(GO_TARGET)

clean:
	rm -rf $(GO_BUILD_DIR)
