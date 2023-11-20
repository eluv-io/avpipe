include ../rules.make
TOP_DIR ?= $(shell pwd)

SRCS=elv_xc.c elv_mux.c
LIBDIRS=-L$(TOP_DIR)/../lib -L/usr/local/lib
INCDIRS=-I$(TOP_DIR)/../include
FLAGS=-O0 -ggdb -Wall -fPIC

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

all: exc install

install:
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo Installing exc
	@cp $(BINDIR)/exc $(TOP_DIR)/../$(BINDIR)

exc: $(OBJS)
	@echo Making exc
	gcc $? ${CFLAGS} ${LDFLAGS} ${LIBDIRS} ${LIBS} -o $(BINDIR)/exc

$(BINDIR)/%.o: %.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${CFLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR)

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif
