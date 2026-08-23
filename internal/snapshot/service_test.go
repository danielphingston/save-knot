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
