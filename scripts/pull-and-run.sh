#!/usr/bin/env bash
#
# pull-and-run.sh — update claude-bridge from GitHub and run it.
#
# For the dad-PC source-build deploy (no Apple Developer ID, so the binary is
# compiled locally rather than self-updated). Pulls the latest commit on the
# current branch, rebuilds with CGO, and launches the app.
#
# Usage:
#   ./scripts/pull-and-run.sh            # pull current branch, build, run
#   BRANCH=main ./scripts/pull-and-run.sh  # force a branch first
#
# Prereqs: git, Go, Xcode Command Line Tools (clang for CGO), and the `claude`
# CLI installed + authed (the dispatcher shells out to it).

set -euo pipefail

# Resolve repo root from this script's location so it works from any CWD.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

BINARY="claude-bridge"

if ! command -v go >/dev/null 2>&1; then
	echo "error: 'go' not found. Install Go (brew install go) and retry." >&2
	exit 1
fi

# Optional: switch branch before pulling.
if [[ -n "${BRANCH:-}" ]]; then
	echo ">> checkout $BRANCH"
	git checkout "$BRANCH"
fi

echo ">> git pull (fast-forward only)"
git pull --ff-only

echo ">> build ($(git describe --tags --always --dirty 2>/dev/null || echo dev))"
CGO_ENABLED=1 go build -o "$BINARY" .

echo ">> run ./$BINARY"
exec "./$BINARY"
