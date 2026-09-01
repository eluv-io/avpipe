TOP_DIR ?= $(shell pwd)
SRCS=elv_log.c elv_time.c elv_channel.c url_parser.c elv_sock.c base64.c

BINDIR=bin
LIBDIR=lib
INCDIR=include

OSNAME := $(shell uname -s)

INCDIRS=-I${FFMPEG_DIST}/include -Iinclude

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

LIBDIRS=-L${FFMPEG_DIST}/lib
FLAGS=-O0 -ggdb -Wall -fPIC

ifeq ($(OSNAME), Darwin)
	SHARED_FLAGS=-dynamiclib
	SHARED_LIB=libutils.dylib
endif
ifeq ($(OSNAME), Linux)
	SHARED_FLAGS=-shared
	SHARED_LIB=libutils.so
endif

all: install

install: libs
	@mkdir -p $(TOP_DIR)/../$(LIBDIR)
	@echo Installing libutils.a
	@cp $(LIBDIR)/* $(TOP_DIR)/../$(LIBDIR)
	@mkdir -p $(TOP_DIR)/../$(INCDIR)
	@echo Installing includes
	@cp $(INCDIR)/* $(TOP_DIR)/../$(INCDIR)

libs: $(OBJS)
	@mkdir -p $(LIBDIR)
	@echo Making libutils.a
	@ar cr $(LIBDIR)/libutils.a $?
	@ranlib $(LIBDIR)/libutils.a

sharedlibs: $(OBJS)
	@mkdir -p $(LIBDIR)
	@echo Making ${SHARED_LIB}
	@gcc ${SHARED_FLAGS} -o $(LIBDIR)/${SHARED_LIB} $?

$(BINDIR)/%.o: src/%.c
	@mkdir -p $(BINDIR)
	@echo "Compiling ..." $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR) $(LIBDIR)

tags:
	ctags -R .

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif

