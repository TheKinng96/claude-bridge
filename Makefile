# claude-bridge build tasks.
#
# CGO is required (the systray dep links against macOS Cocoa), so every build
# sets CGO_ENABLED=1. That means Xcode Command Line Tools must be installed:
#   xcode-select --install
#
# Common use:
#   make build     # compile ./claude-bridge
#   make run       # build + run
#   make update    # git pull + build + run   (dad's update cycle)
#   make test      # run the unit tests
#   make release   # build with the git version baked in (enables self-update)

BINARY      := claude-bridge
PKG         := .
CGO         := CGO_ENABLED=1
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS_REL := -ldflags "-X main.version=$(VERSION)"

.PHONY: build run update test tidy clean release version

# Dev build. Leaves main.version = "dev", which disables the self-updater —
# the right default for a machine without an Apple Developer ID.
build:
	$(CGO) go build -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

# Dad's one-command update: pull latest main, rebuild, run.
update:
	git pull --ff-only
	$(CGO) go build -o $(BINARY) $(PKG)
	./$(BINARY)

test:
	$(CGO) go test ./...

tidy:
	go mod tidy

clean:
	rm -f $(BINARY)

# Versioned build — bakes the git tag/sha into main.version so the GitHub
# Releases self-updater will engage. Only useful once release binaries are
# signed/notarized; on an unsigned setup, prefer `make build`.
release:
	$(CGO) go build $(LDFLAGS_REL) -o $(BINARY) $(PKG)

version:
	@echo $(VERSION)
