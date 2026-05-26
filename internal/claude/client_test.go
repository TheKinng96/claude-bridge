package claude

import (
	"strings"
	"testing"
)

func TestClaudeExitError_SurfacesStdoutWhenStderrEmpty(t *testing.T) {
	// `claude --print` often writes its real error to stdout, not stderr.
	// The message must surface it instead of a bare "claude exited 1: ".
	err := claudeExitError(1, nil, []byte("Invalid API key · Please run /login"))
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "exited 1") {
		t.Errorf("missing exit code: %q", msg)
	}
	if !strings.Contains(msg, "Please run /login") {
		t.Errorf("stdout detail not surfaced: %q", msg)
	}
}

func TestClaudeExitError_PrefersStderrButKeepsStdout(t *testing.T) {
	err := claudeExitError(1, []byte("auth error"), []byte("stdout note"))
	msg := err.Error()
	if !strings.Contains(msg, "auth error") {
		t.Errorf("stderr dropped: %q", msg)
	}
	if !strings.Contains(msg, "stdout note") {
		t.Errorf("stdout dropped: %q", msg)
	}
}

func TestClaudeExitError_TruncatesAndTrims(t *testing.T) {
	long := strings.Repeat("x", 1000)
	err := claudeExitError(2, []byte("  "+long+"  "), nil)
	msg := err.Error()
	if strings.Contains(msg, "  x") {
		t.Errorf("detail not trimmed: %q", msg[:40])
	}
	if len(msg) > 400 {
		t.Errorf("detail not truncated, len=%d", len(msg))
	}
}
