TOP_DIR ?= $(shell pwd)/../..

FFMPEG_CFLAGS := $(shell pkg-config --cflags libavcodec libavformat libavutil)
FFMPEG_LIBS   := $(shell pkg-config --libs   libavcodec libavformat libavutil)

CFLAGS := -O0 -ggdb -Wall \
	$(FFMPEG_CFLAGS) \
	-I$(TOP_DIR)/libavpipe/include \
	-I$(TOP_DIR)/utils/include \
	-I$(TOP_DIR)/libavpipe/test

LIBS := $(FFMPEG_LIBS) \
	-L$(TOP_DIR)/lib -lutils

TESTS := test_color_defaults
OUT   := out
BINS  := $(addprefix $(OUT)/,$(TESTS))

.PHONY: all clean

all: $(BINS)
	@for t in $(BINS); do echo "--- $$t ---"; ./$$t; done

$(OUT):
	mkdir -p $(OUT)

$(OUT)/test_color_defaults: test_color_defaults.c unity/unity.c | $(OUT)
	gcc $(CFLAGS) $^ -o $@ $(LIBS)

clean:
	rm -rf out $(TESTS:=.dSYM)
