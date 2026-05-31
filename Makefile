# claude-bridge build tasks.
#
# CGO is required (the systray dep links against macOS Cocoa), so every build
# sets CGO_ENABLED=1. That means Xcode Command Line Tools must be installed:
#   xcode-select --install
#
# Common use:
#   make build     # compile ./claude-bridge
#   make run       # build + run (foreground)
#   make restart   # build + stop running app + relaunch in background
#   make stop      # stop the running background app
#   make update    # git pull + build + run   (dad's update cycle)
#   make test      # run the unit tests
#   make release   # build with the git version baked in (enables self-update)

BINARY      := claude-bridge
PKG         := .
CGO         := CGO_ENABLED=1
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS_REL := -ldflags "-X main.version=$(VERSION)"
LOGFILE     := claude-bridge.log

.PHONY: build run restart stop update test tidy clean release version probe

# Dev build. Leaves main.version = "dev", which disables the self-updater —
# the right default for a machine without an Apple Developer ID.
build:
	$(CGO) go build -o $(BINARY) $(PKG)

run: build
	./$(BINARY)

# Stop the running app, rebuild, relaunch detached in the background (survives
# closing the terminal; logs to $(LOGFILE)). The 'claude-bridge$$' pattern
# matches the app's command line — which ENDS in the binary name — but NOT the
# 'claude-bridge --mcp' helper that Claude.app spawns, so that one is left alone.
restart: build stop
	@nohup ./$(BINARY) >$(LOGFILE) 2>&1 </dev/null &
	@sleep 1
	@echo "claude-bridge running in background (pid $$(pgrep -f 'claude-bridge$$')). Logs: tail -f $(LOGFILE)"

# Stop the background app only (leaves the --mcp helper running). No error if
# nothing is running.
stop:
	@-pkill -f 'claude-bridge$$' 2>/dev/null && echo "stopped running claude-bridge" || echo "no running claude-bridge"
	@sleep 1

# Dad's one-command update: pull latest main, rebuild, run.
update:
	git pull --ff-only
	$(CGO) go build -o $(BINARY) $(PKG)
	./$(BINARY)

test:
	$(CGO) go test ./...

# Loop the dispatcher with the canonical "i put a pdf …" probe message until
# the reply looks healthy (or N attempts in). MSG / RETRIES env vars override.
# Bridge must already be running (make restart / make run).
probe:
	@MSG="$${MSG:-i put a pdf in the knowledge base, can you read it?}"; \
	RETRIES="$${RETRIES:-3}"; \
	echo ">> probe (retries=$$RETRIES): $$MSG"; \
	curl -sS -X POST http://localhost:10002/api/dispatch/test \
	  -H 'Content-Type: application/json' \
	  -d "{\"message\":\"$$MSG\",\"retries\":$$RETRIES}" | python3 -m json.tool

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
