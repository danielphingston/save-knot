package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/database"
)

func TestCreateDeduplicatesAndRestorePreservesCurrentState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := database.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	saveDirectory := filepath.Join(root, "saves")
	if err := os.MkdirAll(saveDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	savePath := filepath.Join(saveDirectory, "slot.dat")
	if err := os.WriteFile(savePath, []byte("first version"), 0o600); err != nil {
		t.Fatal(err)
	}
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	if err := store.AddPath(ctx, core.GamePath{ID: "path-a", GameID: game.ID, Source: "custom", Template: saveDirectory, Resolved: saveDirectory, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	service := New(store, store, filepath.Join(root, "blobs"), "device-a")
	first, err := service.Create(ctx, game)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, game); !errors.Is(err, ErrUnchanged) {
		t.Fatalf("identical content created a redundant snapshot: %v", err)
	}
	game.Notes = "portable metadata changed"
	if _, err := service.Create(ctx, game); err != nil {
		t.Fatalf("metadata-only change did not create a portable snapshot: %v", err)
	}
	if err := os.WriteFile(savePath, []byte("later version"), 0o600); err != nil {
		t.Fatal(err)
	}
	preRestore, err := service.Restore(ctx, game, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preRestore.ID == "" {
		t.Fatal("restore did not create a pre-restore snapshot")
	}
	data, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first version" {
		t.Fatalf("unexpected restored content: %q", data)
	}
}

func TestCreateReturnsNoFilesForMissingPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := database.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "not-created")
	if err := store.AddPath(ctx, core.GamePath{ID: "path-a", GameID: game.ID, Source: "custom", Template: missing, Resolved: missing, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, err = New(store, store, filepath.Join(root, "blobs"), "device-a").Create(ctx, game)
	if !errors.Is(err, ErrNoFiles) {
		t.Fatalf("expected ErrNoFiles, got %v", err)
	}
}

func TestRestoreSucceedsWhenCurrentSaveIsMissing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := database.Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	savePath := filepath.Join(root, "slot.dat")
	if err := os.WriteFile(savePath, []byte("target version"), 0o600); err != nil {
		t.Fatal(err)
	}
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	if err := store.AddPath(ctx, core.GamePath{ID: "path-a", GameID: game.ID, Source: "custom", Template: savePath, Resolved: savePath, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	service := New(store, store, filepath.Join(root, "blobs"), "device-a")
	target, err := service.Create(ctx, game)
	if err != nil {
		t.Fatal(err)
	}
	if target.Registry != nil {
		t.Fatalf("target unexpectedly includes registry state: %#v", target.Registry)
	}
	if err := os.Remove(savePath); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, game); !errors.Is(err, ErrNoFiles) {
		t.Fatalf("create with missing current save = %v, want ErrNoFiles", err)
	}

	preRestore, err := service.Restore(ctx, game, target.ID)
	if err != nil {
		t.Fatalf("restore with missing current save failed: %v", err)
	}
	if preRestore.ID != "" {
		t.Fatalf("pre-restore snapshot = %q, want zero snapshot after ErrNoFiles", preRestore.ID)
	}
	got, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("restored save file was not recreated: %v", err)
	}
	if string(got) != "target version" {
		t.Fatalf("restored save = %q, want %q", got, "target version")
	}
}
