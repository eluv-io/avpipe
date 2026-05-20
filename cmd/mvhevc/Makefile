# Makefile for mvhevc
#
# Paths assume:
#   - FFmpeg/x265 development packages are visible to pkg-config
#   - x265 built with ENABLE_MULTIVIEW=1 in elv-toolchain
#
# Build: make
# Clean: make clean

TOP_DIR ?= $(shell pwd)
ROOT_DIR ?= $(TOP_DIR)/../..
BINDIR ?= bin
UNAME_S := $(shell uname -s)

# avpipe include path
AVPIPE_INCLUDE = ../../include

# FFmpeg/x265 lookup uses the caller's pkg-config environment.
FFMPEG_PKGS   = libavformat libavcodec libavutil libswscale libavfilter libswresample
X265_PKGS     = x265
FFMPEG_CFLAGS = $(shell pkg-config --cflags $(FFMPEG_PKGS))
FFMPEG_LDFLAGS = $(shell pkg-config --libs $(FFMPEG_PKGS))

# x265 flags - use the multiview-enabled build
X265_CFLAGS  = $(shell pkg-config --cflags $(X265_PKGS))
X265_LDFLAGS = $(shell pkg-config --libs $(X265_PKGS)) -Wl,-rpath

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

.PHONY: all install clean run-test check-pkg-config

all: $(BINARIES) install

check-pkg-config:
	@command -v pkg-config >/dev/null 2>&1 || { \
		echo "error: pkg-config not found"; \
		exit 1; \
	}
	@pkg-config --exists $(FFMPEG_PKGS) $(X265_PKGS) || { \
		echo "error: missing pkg-config packages for mvhevc_encoder"; \
		echo "required: $(FFMPEG_PKGS) $(X265_PKGS)"; \
		echo "set PKG_CONFIG_PATH to your FFmpeg/x265 pkgconfig directory if needed"; \
		pkg-config --print-errors --exists $(FFMPEG_PKGS) $(X265_PKGS); \
		exit 1; \
	}

$(TARGET): check-pkg-config $(OBJS)
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

%.o: %.c mvhevc_xc.h check-pkg-config
	$(CC) $(CFLAGS) -c -o $@ $<

clean:
	rm -rf $(TARGET) $(GO_BUILD_DIR) *.o *.hevc

# Quick test with sample files (update paths as needed)
run-test: $(TARGET)
	@echo "Running test encode..."
	./$(TARGET) left.mp4 right.mp4 test_mvhevc.hevc 28
