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

func TestWrite_ReplaceCreatesFileAndDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Vault") // not yet created
	s := New(dir)
	if err := s.Write("first", "replace"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if got != "first" {
		t.Fatalf("want %q got %q", "first", got)
	}
	if err := s.Write("second", "replace"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Read()
	if got != "second" {
		t.Fatalf("replace: want %q got %q", "second", got)
	}
}

func TestWrite_Append(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Write("line1", "replace"); err != nil {
		t.Fatal(err)
	}
	if err := s.Write("line2", "append"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if got != "line1\nline2" {
		t.Fatalf("append: want %q got %q", "line1\nline2", got)
	}
}

func TestWrite_DefaultModeIsReplace(t *testing.T) {
	s := New(t.TempDir())
	_ = s.Write("a", "replace")
	if err := s.Write("b", ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Read()
	if got != "b" {
		t.Fatalf("empty mode should replace: got %q", got)
	}
}

func TestWrite_Disabled(t *testing.T) {
	if err := New("").Write("x", "replace"); !errors.Is(err, ErrDisabled) {
		t.Fatalf("disabled Write: want ErrDisabled, got %v", err)
	}
}
