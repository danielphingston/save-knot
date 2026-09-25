package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/database"
)

func TestRestoreDoesNotChangeEarlierFilesWhenLaterBlobIsMissing(t *testing.T) {
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
	saves := filepath.Join(root, "saves")
	if err := os.Mkdir(saves, 0o700); err != nil {
		t.Fatal(err)
	}
	game := core.Game{ID: "game", DisplayName: "Game", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	if err := store.AddPath(ctx, core.GamePath{ID: "path", GameID: game.ID, Source: "custom", Template: saves, Resolved: saves, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"a.dat": "old a", "b.dat": "old b"} {
		if err := os.WriteFile(filepath.Join(saves, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	service := New(store, store, filepath.Join(root, "blobs"), "device")
	target, err := service.Create(ctx, game)
	if err != nil {
		t.Fatal(err)
	}
	if len(target.Files) != 2 || target.Files[0].Path != "a.dat" || target.Files[1].Path != "b.dat" {
		t.Fatalf("unexpected test snapshot file order: %#v", target.Files)
	}
	missing, err := store.BlobPath(ctx, target.Files[1].Hash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(missing); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"a.dat": "current a", "b.dat": "current b"} {
		if err := os.WriteFile(filepath.Join(saves, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Restore(ctx, game, target.ID); err == nil {
		t.Fatal("restore unexpectedly succeeded despite missing target blob")
	}
	assertSaveFiles(t, saves, map[string]string{"a.dat": "current a", "b.dat": "current b"})
}

func assertSaveFiles(t *testing.T, saves string, expected map[string]string) {
	t.Helper()
	for name, want := range expected {
		got, err := os.ReadFile(filepath.Join(saves, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("failed restore changed %s to %q, want %q", name, got, want)
		}
	}
}
