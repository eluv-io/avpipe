TOP_DIR ?= $(shell pwd)/../..

FFMPEG_CFLAGS := $(shell pkg-config --cflags libavcodec libavformat libavutil)
FFMPEG_LIBS   := $(shell pkg-config --libs   libavcodec libavformat libavutil)

# avpipe_filters.c (included directly by test_fade_filter) pulls in libavfilter.
# Only -l is needed: LIBS already supplies -L/-rpath for the same FFmpeg dist.
AVFILTER_LIBS := $(shell pkg-config --libs-only-l libavfilter)

CFLAGS := -O0 -ggdb -Wall \
	$(FFMPEG_CFLAGS) \
	-I$(TOP_DIR)/libavpipe/include \
	-I$(TOP_DIR)/utils/include \
	-I$(TOP_DIR)/libavpipe/test

LIBS := $(FFMPEG_LIBS) \
	-L$(TOP_DIR)/lib -lutils

TESTS := test_color_defaults test_hdr_metadata test_pts_unwrap test_fade_filter \
	test_vertical_crop
OUT   := out
BINS  := $(addprefix $(OUT)/,$(TESTS))

# Each test #includes the production .c file it exercises, so that source - and
# the headers it pulls in - are real prerequisites of the test binary. Listing
# them matters: without it make considers a binary up to date after a library
# change and silently re-runs the stale one against the old code.
#
# They deliberately do not go in $^, which is why each recipe names its sources
# explicitly ($< plus unity). The production .c is already #included by the test,
# so handing it to gcc a second time would give duplicate symbols.
#
# -MMD is not usable here: with several sources and a single -o, gcc writes one
# dep file and keeps only the last source's dependencies.
SRC_DIR   := $(TOP_DIR)/libavpipe/src
PROD_HDRS := $(wildcard $(TOP_DIR)/libavpipe/include/*.h) \
	$(wildcard $(TOP_DIR)/utils/include/*.h)

.PHONY: all clean

all: $(BINS)
	@for t in $(BINS); do echo "--- $$t ---"; ./$$t; done

$(OUT):
	mkdir -p $(OUT)

$(OUT)/test_color_defaults: test_color_defaults.c unity/unity.c \
		$(SRC_DIR)/avpipe_format.c $(PROD_HDRS) | $(OUT)
	gcc $(CFLAGS) $< unity/unity.c -o $@ $(LIBS)

$(OUT)/test_hdr_metadata: test_hdr_metadata.c unity/unity.c \
		$(SRC_DIR)/avpipe_codec.c $(PROD_HDRS) | $(OUT)
	gcc $(CFLAGS) $< unity/unity.c -o $@ $(LIBS)

$(OUT)/test_pts_unwrap: test_pts_unwrap.c unity/unity.c \
		$(SRC_DIR)/avpipe_format.c $(PROD_HDRS) | $(OUT)
	gcc $(CFLAGS) $< unity/unity.c -o $@ $(LIBS)

$(OUT)/test_fade_filter: test_fade_filter.c unity/unity.c \
		$(SRC_DIR)/avpipe_filters.c $(PROD_HDRS) | $(OUT)
	gcc $(CFLAGS) $< unity/unity.c -o $@ $(LIBS) $(AVFILTER_LIBS)

# -lm is required on glibc for sqrt/fabs/fmod; on macOS libm is part of libSystem
# and links implicitly, so leaving it off builds there but fails on Linux.
$(OUT)/test_vertical_crop: test_vertical_crop.c unity/unity.c \
		$(SRC_DIR)/avpipe_filters.c $(SRC_DIR)/avpipe_utils.c $(PROD_HDRS) | $(OUT)
	gcc $(CFLAGS) $< unity/unity.c -o $@ $(LIBS) $(AVFILTER_LIBS) -lm

clean:
	rm -rf out $(TESTS:=.dSYM)
