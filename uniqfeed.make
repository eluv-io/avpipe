UNIQFEED_DIST ?= $(HOME)/.local
UNIQFEED_LIBDIRS = -L$(UNIQFEED_DIST)/lib -L$(UNIQFEED_DIST)/lib/uf -L$(UNIQFEED_DIST)/lib/3rdparty
UNIQFEED_RPATHLINKS = -Wl,-rpath-link,$(UNIQFEED_DIST)/lib -Wl,-rpath-link,$(UNIQFEED_DIST)/lib/uf -Wl,-rpath-link,$(UNIQFEED_DIST)/lib/3rdparty
UNIQFEED_AUTO_LIBS = $(shell ([ -f $(UNIQFEED_DIST)/lib/libuf-renderlib.so ] && echo $(UNIQFEED_DIST)/lib/libuf-renderlib.so; find $(UNIQFEED_DIST)/lib/uf $(UNIQFEED_DIST)/lib/3rdparty -maxdepth 1 -type f -name 'lib*.so*') | sort | sed 's#.*/##;s#^#-l:#' | tr '\n' ' ')