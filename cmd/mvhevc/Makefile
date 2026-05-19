# Makefile for mvhevc
#
# Paths assume:
#   - FFmpeg built and installed to ~/.local (pkg-config available)
#   - x265 built with ENABLE_MULTIVIEW=1 in elv-toolchain-7.1
#
# Build: make
# Clean: make clean

TOP_DIR ?= $(shell pwd)
ROOT_DIR ?= $(TOP_DIR)/../..
BINDIR ?= bin
UNAME_S := $(shell uname -s)

# avpipe include path
AVPIPE_INCLUDE = ../../include

# Use local FFmpeg install via pkg-config
PKG_CONFIG_PATH := $(HOME)/.local/lib/pkgconfig
FFMPEG_CFLAGS  := $(shell PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) pkg-config --cflags libavformat libavcodec libavutil libswscale libavfilter libswresample)
FFMPEG_LDFLAGS := $(shell PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) pkg-config --libs   libavformat libavcodec libavutil libswscale libavfilter libswresample)

# x265 flags - use the multiview-enabled build
X265_CFLAGS  := $(shell PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) pkg-config --cflags x265)
X265_LDFLAGS := $(shell PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) pkg-config --libs   x265) -Wl,-rpath

CC = clang
OBJCXX = clang++
CFLAGS = -Wall -Wextra -g -O2 -I$(AVPIPE_INCLUDE) $(FFMPEG_CFLAGS) $(X265_CFLAGS)
LDFLAGS = $(FFMPEG_LDFLAGS) $(X265_LDFLAGS) -lc++ -lpthread
OBJCXXFLAGS = -Wall -Wextra -g -O2 -std=c++17 -fobjc-arc -mmacosx-version-min=14.0
APPLE_FRAMEWORKS = -framework Foundation -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework VideoToolbox

TARGET       = mvhevc_encoder
APPLE_TARGET = mvhevc_apple
GO_TARGET    = mvhevc
GO_BUILD_DIR = .build
GO_BINARY    = $(GO_BUILD_DIR)/$(GO_TARGET)
APPLE_BINARY = $(GO_BUILD_DIR)/$(APPLE_TARGET)
GO_SRCS      = $(shell find mvhevc ../../mp4e/mvhevc -name '*.go')
APPLE_SRCS   = apple/mvhevc_apple.mm
SRCS         = mvhevc_encoder.c mvhevc_xc.c
OBJS         = $(SRCS:.c=.o)
BINARIES     = $(TARGET) $(GO_BINARY)
INSTALL_TARGETS = install-common

ifeq ($(UNAME_S),Darwin)
BINARIES += $(APPLE_BINARY)
INSTALL_TARGETS += install-apple
endif

.PHONY: all install clean run-test

all: $(BINARIES) install

$(TARGET): $(OBJS)
	$(CC) $(CFLAGS) -o $@ $(OBJS) $(LDFLAGS)
	@echo ""
	@echo "Build successful: $(TARGET)"

$(GO_BINARY): $(GO_SRCS) ../../go.mod ../../go.sum
	@(if [ ! -d $(GO_BUILD_DIR) ]; then mkdir $(GO_BUILD_DIR); fi)
	go build -v -x -o $(GO_BINARY) ./mvhevc

$(APPLE_BINARY): $(APPLE_SRCS)
	@(if [ ! -d $(GO_BUILD_DIR) ]; then mkdir $(GO_BUILD_DIR); fi)
	$(OBJCXX) $(OBJCXXFLAGS) -o $(APPLE_BINARY) $(APPLE_SRCS) $(APPLE_FRAMEWORKS)

install: $(INSTALL_TARGETS)

install-common: $(TARGET) $(GO_BINARY)
	@(if [ ! -d $(ROOT_DIR)/$(BINDIR) ]; then mkdir $(ROOT_DIR)/$(BINDIR); fi)
	@echo "Installing $(TARGET)"
	@cp $(TARGET) $(ROOT_DIR)/$(BINDIR)
	@echo "Installing $(GO_TARGET)"
	@cp $(GO_BINARY) $(ROOT_DIR)/$(BINDIR)/$(GO_TARGET)

install-apple: $(APPLE_BINARY)
	@(if [ ! -d $(ROOT_DIR)/$(BINDIR) ]; then mkdir $(ROOT_DIR)/$(BINDIR); fi)
	@echo "Installing $(APPLE_TARGET)"
	@cp $(APPLE_BINARY) $(ROOT_DIR)/$(BINDIR)/$(APPLE_TARGET)

%.o: %.c mvhevc_xc.h
	$(CC) $(CFLAGS) -c -o $@ $<

clean:
	rm -rf $(TARGET) $(GO_BUILD_DIR) *.o *.hevc

# Quick test with sample files (update paths as needed)
run-test: $(TARGET)
	@echo "Running test encode..."
	./$(TARGET) left.mp4 right.mp4 test_mvhevc.hevc 28
