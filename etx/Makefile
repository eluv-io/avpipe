include ../rules.make
TOP_DIR ?= $(shell pwd)

SRCS=elv_xc.c elv_mux.c
LIBDIRS=-L$(TOP_DIR)/../lib -L/usr/local/lib
INCDIRS=-I$(TOP_DIR)/../include
FLAGS=-O0 -ggdb -Wall -fPIC -fstack-check

OBJS=$(SRCS:%.c=$(BINDIR)/%.o)

all: etx install

install:
	@(if [ ! -d $(TOP_DIR)/../$(BINDIR) ]; then mkdir $(TOP_DIR)/../$(BINDIR); fi)
	@echo Installing etx
	@cp $(BINDIR)/etx $(TOP_DIR)/../$(BINDIR)

etx: $(OBJS)
	@echo Making etx
	gcc $? ${LIBDIRS} ${LIBS} -o $(BINDIR)/etx

$(BINDIR)/%.o: %.c
	@(if [ ! -d $(BINDIR) ]; then mkdir $(BINDIR); fi)
	@echo "Compiling " $<
	gcc ${FLAGS} ${INCDIRS} -c $< -o $@

clean:
	@rm -rf $(BINDIR)

check-env:
ifndef FFMPEG_DIST
  $(error FFMPEG_DIST is undefined)
endif

