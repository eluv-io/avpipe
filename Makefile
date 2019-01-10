SUBDIRS=utils libavpipe

all clean: check-env
	@for dir in $(SUBDIRS); do \
	echo "Making $@ in $$dir..."; \
	(cd $$dir; make $@) || exit 1; \
	done

check-env:
ifndef ELV_TOOLCHAIN_DIST_PLATFORM
  $(error ELV_TOOLCHAIN_DIST_PLATFORM is undefined)
endif

