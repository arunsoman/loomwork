# Build and install loomwork from this source tree.
#
#   make install                  install to ~/.local/bin
#   make install BINDIR=/usr/local/bin
#   make uninstall
BINDIR ?= $(HOME)/.local/bin

.PHONY: build install uninstall

build:
	go build -trimpath -o loomwork .

install:
	mkdir -p "$(BINDIR)"
	go build -trimpath -o "$(BINDIR)/loomwork" .
	@echo "✓ Installed: $(BINDIR)/loomwork"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) echo "  Note: $(BINDIR) is not on your PATH. Add: export PATH=\"$(BINDIR):\$$PATH\"";; esac

uninstall:
	rm -f "$(BINDIR)/loomwork"
