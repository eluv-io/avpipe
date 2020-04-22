TOP_DIR ?= $(shell pwd)
SRCS=elv_log.c elv_time.c elv_channel.c url_parser.c elv_sock.c base64.c

BINDIR=bin
LIBDIR=lib
INCDIR=include

OSNAME := $(shell uname -s)

INCDIRS=-I${ELV_TOOLCHAIN_DIST_PLATFORM}/include -Iinclude

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

LIBDIRS=-L${ELV_TOOLCHAIN_DIST_PLATFORM}/lib
FLAGS=-O0 -ggdb -Wall -fPIC

ifeq ($(OSNAME), Darwin)
	SHARED_FLAGS=-dynamiclib
	SHARED_LIB=libutils.dylib
endif
ifeq ($(OSNAME), Linux)
	SHARED_FLAGS=-shared
	SHARED_LIB=libutils.so
endif

all: libs install

install:
	@(if [ ! -d $(TOP_DIR)/../$(LIBDIR) ]; then mkdir $(TOP_DIR)/../$(LIBDIR); fi)
	@echo Installing libutils.a
	@cp $(LIBDIR)/* $(TOP_DIR)/../$(LIBDIR)
	@(if [ ! -d $(TOP_DIR)/../$(INCDIR) ]; then mkdir $(TOP_DIR)/../$(INCDIR); fi)
	@echo Installing includes
	@cp $(INCDIR)/* $(TOP_DIR)/../$(INCDIR)

libs: $(OBJS)
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@echo Making libutils.a
	@ar cr $(LIBDIR)/libutils.a $?
	@ranlib $(LIBDIR)/libutils.a

sharedlibs: $(OBJS)
	@(if [ ! -d $(LIBDIR) ]; then mkdir $(LIBDIR); fi)
	@echo Making ${SHARED_LIB}
	@gcc ${SHARED_FLAGS} -o $(LIBDIR)/${SHARED_LIB} $?

$(BINDIR)/%.o: src/%.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling ..." $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR) $(LIBDIR)

tags:
	ctags -R .

check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

