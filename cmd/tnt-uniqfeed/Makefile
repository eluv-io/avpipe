TOP_DIR := ../..

APP := tnt-uniqfeed
SRC := main.c
OBJDIR := .obj
OBJ := $(OBJDIR)/main.o
OUT := $(TOP_DIR)/bin/$(APP)
CONTAINER_SCRIPT := ./uniqfeed-container.sh

DEFAULT_FFMPEG_DIST := $(abspath $(TOP_DIR)/../FFmpeg/dist)
UF_BASE_IMAGE ?= uf_render_interface:ubuntu_22

PKG_CONFIG ?= pkg-config
UF_RUNTIME_DIR ?= /runtime

FFMPEG_PKG_LIBS := libavfilter libavcodec libavformat libavdevice libswresample libavresample libswscale libavutil libpostproc

FFMPEG_TREE_LIB_DIRS := \
	$(FFMPEG_DIST)/libavfilter \
	$(FFMPEG_DIST)/libavcodec \
	$(FFMPEG_DIST)/libavformat \
	$(FFMPEG_DIST)/libavdevice \
	$(FFMPEG_DIST)/libswresample \
	$(FFMPEG_DIST)/libavresample \
	$(FFMPEG_DIST)/libswscale \
	$(FFMPEG_DIST)/libavutil \
	$(FFMPEG_DIST)/libpostproc

FFMPEG_TREE_LDLIBS := -lavfilter -lavcodec -lavformat -lavdevice -lswresample -lavresample -lswscale -lavutil -lpostproc

FFMPEG_TREE_LDFLAGS := $(foreach dir,$(FFMPEG_TREE_LIB_DIRS),-L$(dir) -Wl,-rpath,$(dir) -Wl,-rpath-link,$(dir)) $(FFMPEG_TREE_LDLIBS)

ifeq ($(wildcard $(FFMPEG_DIST)/libavcodec/avcodec.h),)
FFMPEG_USE_TREE := 0
else
FFMPEG_USE_TREE := 1
endif

UF_CFLAGS := -DUSE_UF_RENDERLIB -I$(UF_RUNTIME_DIR)/include
UF_LDFLAGS := \
	-L$(UF_RUNTIME_DIR)/lib \
	-L$(UF_RUNTIME_DIR)/lib/uf \
	-L$(UF_RUNTIME_DIR)/lib/3rdparty \
	-Wl,-rpath,$(UF_RUNTIME_DIR)/lib \
	-Wl,-rpath,$(UF_RUNTIME_DIR)/lib/uf \
	-Wl,-rpath,$(UF_RUNTIME_DIR)/lib/3rdparty \
	-Wl,-rpath-link,$(UF_RUNTIME_DIR)/lib \
	-Wl,-rpath-link,$(UF_RUNTIME_DIR)/lib/uf \
	-Wl,-rpath-link,$(UF_RUNTIME_DIR)/lib/3rdparty \
	-Wl,--unresolved-symbols=ignore-in-shared-libs \
	-luf-renderlib

ifeq ($(FFMPEG_USE_TREE),1)
FFMPEG_CFLAGS := -I$(FFMPEG_DIST)
FFMPEG_LDFLAGS := $(FFMPEG_TREE_LDFLAGS)
else
FFMPEG_CFLAGS := $(shell $(PKG_CONFIG) --cflags $(FFMPEG_PKG_LIBS))
FFMPEG_LDFLAGS := $(shell $(PKG_CONFIG) --libs $(FFMPEG_PKG_LIBS))
endif

CFLAGS += -Wall -Wextra -g -O0 $(UF_CFLAGS) $(FFMPEG_CFLAGS)
LDFLAGS += $(FFMPEG_LDFLAGS) $(UF_LDFLAGS)

.PHONY: all build local-build run shell clean local-clean check-env

.DEFAULT_GOAL := build

all: build

build:
	UF_BASE_IMAGE="$(UF_BASE_IMAGE)" $(CONTAINER_SCRIPT) build

run:
ifndef INPUT
	$(error INPUT is undefined)
endif
ifndef OUTPUT
	$(error OUTPUT is undefined)
endif
ifndef PROJECT
	$(error PROJECT is undefined)
endif
ifndef METADATA
	$(error METADATA is undefined)
endif
	UF_BASE_IMAGE="$(UF_BASE_IMAGE)" $(CONTAINER_SCRIPT) run "$(INPUT)" "$(OUTPUT)" "$(PROJECT)" "$(METADATA)"

shell:
	UF_BASE_IMAGE="$(UF_BASE_IMAGE)" $(CONTAINER_SCRIPT) shell

local-build: check-env $(OUT)

$(OUT): $(OBJ)
	@mkdir -p $(dir $@)
	$(CC) $(OBJ) -o $@ $(LDFLAGS)

$(OBJ): $(SRC)
	@mkdir -p $(OBJDIR)
	$(CC) $(CFLAGS) -c $< -o $@

check-env:
	@if [ "$(FFMPEG_USE_TREE)" = "1" ]; then \
		test -f "$(FFMPEG_DIST)/libavcodec/avcodec.h" || \
			(echo "FFmpeg build-tree headers not found under $(FFMPEG_DIST)" >&2; exit 1); \
	else \
		test -n "$(PKG_CONFIG_PATH)" || \
			(echo "PKG_CONFIG_PATH is undefined; point it at the FFmpeg pkg-config directory before building" >&2; exit 1); \
		$(PKG_CONFIG) --exists $(FFMPEG_PKG_LIBS) || \
			(echo "FFmpeg pkg-config entries not found in the active pkg-config search path (PKG_CONFIG_PATH=$(PKG_CONFIG_PATH))" >&2; exit 1); \
	fi
	@test -d $(UF_RUNTIME_DIR)/include || \
		(echo "uniqFEED include path not found at $(UF_RUNTIME_DIR)/include inside container" >&2; exit 1)

local-clean:
	rm -rf $(OBJDIR) $(OUT)

clean: local-clean
