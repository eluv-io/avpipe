include ../rules.make
TOP_DIR ?= $(shell pwd)

SRCS=elv_xc_test.c
LIBDIRS=-L$(TOP_DIR)/../lib
INCDIRS=-I$(TOP_DIR)/../include
FLAGS=-O0 -ggdb -Wall -fPIC

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
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

