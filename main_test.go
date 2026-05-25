package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"claude-bridge/internal/knowledge"
	"claude-bridge/internal/store"
)

func TestEnsureKnowledgeFolder_CreatesAndSaves(t *testing.T) {
	ctx := context.Background()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer st.Close()

	folder := filepath.Join(t.TempDir(), "Claude Bridge Knowledge")
	got := ensureKnowledgeFolder(ctx, st, knowledge.Config{}, folder)

	if got.FolderPath != folder {
		t.Fatalf("FolderPath = %q, want %q", got.FolderPath, folder)
	}
	for _, p := range []string{
		folder,
		filepath.Join(folder, "Vault"),
		filepath.Join(folder, ".obsidian"),
		filepath.Join(folder, "Welcome.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %q to exist: %v", p, err)
		}
	}

	saved, err := knowledge.LoadConfig(ctx, st)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if saved.FolderPath != folder {
		t.Errorf("persisted FolderPath = %q, want %q", saved.FolderPath, folder)
	}
}

func TestEnsureKnowledgeFolder_RespectsExisting(t *testing.T) {
	ctx := context.Background()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer st.Close()

	preset := "/existing/owner/folder"
	folder := filepath.Join(t.TempDir(), "Claude Bridge Knowledge")
	got := ensureKnowledgeFolder(ctx, st, knowledge.Config{FolderPath: preset}, folder)

	if got.FolderPath != preset {
		t.Errorf("FolderPath = %q, want unchanged %q", got.FolderPath, preset)
	}
	if _, err := os.Stat(folder); !os.IsNotExist(err) {
		t.Errorf("default folder %q should not be created when one is preset", folder)
	}
}
