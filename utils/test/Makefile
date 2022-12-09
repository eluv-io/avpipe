include ../../rules.make
TOP_DIR ?= $(shell pwd)
SRCS=elv_channel_test.c

BINDIR=.
OSNAME := $(shell uname -s)
INCDIRS=-I${FFMPEG_DIST}/include -I../include

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

.PHONY: all

LIBDIRS=-L${FFMPEG_DIST}/lib -L../lib -L../../lib
FLAGS=-O0 -ggdb -Wall -fPIC

ifeq ($(OSNAME), Darwin)
	SHARED_FLAGS=-dynamiclib
	SHARED_LIB=libutils.dylib
endif
ifeq ($(OSNAME), Linux)
	SHARED_FLAGS=-shared
	SHARED_LIB=libutils.so
endif

all: elv_channel_test

elv_channel_test: elv_channel_test.o
	@echo "Making " $@
	gcc $? ${LIBDIRS} ${LIBS} -o $(BINDIR)/elv_channel_test

$(BINDIR)/%.o: ./%.c
	@echo "Compiling ..." $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf *.o elv_channel_test

tags:
	ctags -R .

#check-env:
#ifndef FFMPEG_DIST
#  $(error FFMPEG_DIST is undefined)
#endif

