package ownerprofile

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRead_Missing(t *testing.T) {
	s := New(t.TempDir())
	got, err := s.Read()
	if err != nil {
		t.Fatalf("Read missing: unexpected err %v", err)
	}
	if got != "" {
		t.Fatalf("Read missing: want empty, got %q", got)
	}
}

func TestRead_Present(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProfileFilename), []byte("hello dad"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(dir)
	got, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello dad" {
		t.Fatalf("want %q got %q", "hello dad", got)
	}
}

func TestDisabled(t *testing.T) {
	s := New("")
	if s.Enabled() {
		t.Fatal("empty path should be disabled")
	}
	if _, err := s.Read(); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled Read: want ErrDisabled, got %v", err)
	}
}
