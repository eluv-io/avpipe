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
CFLAGS = -Wall -Wextra -g -O2 -I$(AVPIPE_INCLUDE) $(FFMPEG_CFLAGS) $(X265_CFLAGS)
LDFLAGS = $(FFMPEG_LDFLAGS) $(X265_LDFLAGS) -lc++ -lpthread

TARGET = mvhevc_encoder
SRCS   = mvhevc_encoder.c mvhevc_xc.c
OBJS   = $(SRCS:.c=.o)

.PHONY: all install clean run-test

all: $(TARGET) install

$(TARGET): $(OBJS)
	$(CC) $(CFLAGS) -o $@ $(OBJS) $(LDFLAGS)
	@echo ""
	@echo "Build successful: $(TARGET)"

install: $(TARGET)
	@(if [ ! -d $(ROOT_DIR)/$(BINDIR) ]; then mkdir $(ROOT_DIR)/$(BINDIR); fi)
	@echo "Installing $(TARGET)"
	@cp $(TARGET) $(ROOT_DIR)/$(BINDIR)

%.o: %.c mvhevc_xc.h
	$(CC) $(CFLAGS) -c -o $@ $<

clean:
	rm -f $(TARGET) *.o *.hevc

# Quick test with sample files (update paths as needed)
run-test: $(TARGET)
	@echo "Running test encode..."
	./$(TARGET) left.mp4 right.mp4 test_mvhevc.hevc 28
