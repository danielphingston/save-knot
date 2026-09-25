package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/saveknot/saveknot/internal/core"
)

func TestRelocationUsesSnapshotWriterLayout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	cleanupStoreOnTestExit(t, store)
	payload := []byte("save to relocate")
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(payload, nil)
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(root, "old", hash[:2], hash+".zst")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlob(ctx, hash, oldPath, int64(len(payload)), int64(len(compressed))); err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(root, "new")
	if err := store.RelocateBlobs(ctx, newRoot); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(newRoot, hash[:2], hash+".zst")
	got, err := store.BlobPath(ctx, hash)
	if err != nil || got != want {
		t.Fatalf("relocated path = %q, err = %v; want %q", got, err, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("relocated blob is not readable: %v", err)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("source blob was removed before relocation could be rolled back: %v", err)
	}
}

func TestDeleteSnapshotRetriesFailedOrphanRemovalWithoutDeletingSharedBlob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	cleanupStoreOnTestExit(t, store)
	game := core.Game{ID: "game", DisplayName: "Game", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	orphanPath, sharedPath, blocker := createOrphanRetryFixtures(t, ctx, root, store, game)
	if err := store.DeleteSnapshot(ctx, "old"); err == nil {
		t.Fatal("orphan removal unexpectedly succeeded")
	}
	if path, err := store.BlobPath(ctx, "orphan"); err != nil || path != orphanPath {
		t.Fatalf("failed orphan removal lost retry information: path=%q err=%v", path, err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSnapshot(ctx, "next"); err != nil {
		t.Fatalf("retry could not reclaim orphan: %v", err)
	}
	if _, err := store.BlobPath(ctx, "orphan"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("orphan remained indexed after retry: %v", err)
	}
	if _, err := os.Stat(orphanPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan remained on disk after retry: %v", err)
	}
	if path, err := store.BlobPath(ctx, "shared"); err != nil || path != sharedPath {
		t.Fatalf("shared blob was removed while still referenced: path=%q err=%v", path, err)
	}
	if _, err := os.Stat(sharedPath); err != nil {
		t.Fatalf("shared blob was removed from disk: %v", err)
	}
}

func cleanupStoreOnTestExit(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
}

func createOrphanRetryFixtures(t *testing.T, ctx context.Context, root string, store *Store, game core.Game) (orphanPath, sharedPath, blocker string) {
	t.Helper()
	orphanPath = filepath.Join(root, "blocked-orphan.zst")
	if err := os.Mkdir(orphanPath, 0o700); err != nil {
		t.Fatal(err)
	}
	blocker = filepath.Join(orphanPath, "blocker")
	if err := os.WriteFile(blocker, []byte("force os.Remove to fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	sharedPath = filepath.Join(root, "shared.zst")
	if err := os.WriteFile(sharedPath, []byte("shared"), 0o600); err != nil {
		t.Fatal(err)
	}
	for hash, path := range map[string]string{"orphan": orphanPath, "shared": sharedPath} {
		if err := store.SaveBlob(ctx, hash, path, 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	for index, item := range []struct {
		id     string
		hashes []string
	}{
		{id: "old", hashes: []string{"orphan", "shared"}},
		{id: "next", hashes: []string{"shared"}},
		{id: "last", hashes: []string{"shared"}},
	} {
		files := make([]core.SnapshotFile, 0, len(item.hashes))
		for _, hash := range item.hashes {
			files = append(files, core.SnapshotFile{Hash: hash})
		}
		if err := store.SaveSnapshot(ctx, core.Snapshot{Version: 1, ID: item.id, GameID: game.ID, CreatedAt: time.Unix(int64(index+1), 0), Files: files, RemoteState: "local"}); err != nil {
			t.Fatal(err)
		}
	}
	return orphanPath, sharedPath, blocker
}
