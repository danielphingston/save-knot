package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/saveknot/saveknot/internal/core"
)

func TestSaveBlobRelocatesExistingHashWithoutForgettingUpload(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	const hash = "same-content"
	if err := store.SaveBlob(ctx, hash, "/old/blobs/same-content.zst", 12, 10); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkBlobUploaded(ctx, hash, "account/bucket/prefix"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlob(ctx, hash, "/new/blobs/same-content.zst", 12, 9); err != nil {
		t.Fatal(err)
	}
	path, err := store.BlobPath(ctx, hash)
	if err != nil || path != "/new/blobs/same-content.zst" {
		t.Fatalf("relocated blob path = %q, err = %v", path, err)
	}
	uploaded, err := store.BlobUploaded(ctx, hash, "account/bucket/prefix")
	if err != nil || !uploaded {
		t.Fatalf("relocation lost upload bookkeeping: uploaded=%v err=%v", uploaded, err)
	}
}

func TestDeleteSnapshotReclaimsOnlyUnreferencedLocalBlobs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	game := core.Game{ID: "game", DisplayName: "Game", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{"shared", "old-only", "new-only"} {
		path := filepath.Join(root, hash+".zst")
		if err := os.WriteFile(path, []byte(hash), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveBlob(ctx, hash, path, int64(len(hash)), int64(len(hash))); err != nil {
			t.Fatal(err)
		}
	}
	for _, snapshot := range []core.Snapshot{
		{Version: 1, ID: "old", GameID: game.ID, CreatedAt: time.Unix(1, 0), Files: []core.SnapshotFile{{Hash: "shared"}, {Hash: "old-only"}}, RemoteState: "local"},
		{Version: 1, ID: "new", GameID: game.ID, CreatedAt: time.Unix(2, 0), Files: []core.SnapshotFile{{Hash: "shared"}, {Hash: "new-only"}}, RemoteState: "local"},
	} {
		if err := store.SaveSnapshot(ctx, snapshot); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteSnapshot(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BlobPath(ctx, "old-only"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("orphan blob retained in index: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "old-only.zst")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan blob retained on disk: %v", err)
	}
	for _, hash := range []string{"shared", "new-only"} {
		path, err := store.BlobPath(ctx, hash)
		if err != nil {
			t.Fatalf("live blob %q lost from index: %v", hash, err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("live blob %q lost from disk: %v", hash, err)
		}
	}
}

func TestRelocateBlobsRejectsMismatchedDestinationBeforeSwitchingPath(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	payload := []byte("verified save payload")
	digest := sha256.Sum256(payload)
	hash := strings.ToUpper(hex.EncodeToString(digest[:]))
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(payload, nil)
	encoder.Close()
	source := filepath.Join(root, "source", "blob.zst")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlob(ctx, hash, source, int64(len(payload)), int64(len(compressed))); err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(root, "new")
	destination := filepath.Join(newRoot, "blobs", hash[:2], hash+".zst")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("partial destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RelocateBlobs(ctx, newRoot); err == nil {
		t.Fatal("mismatched destination was trusted")
	}
	path, err := store.BlobPath(ctx, hash)
	if err != nil || path != source {
		t.Fatalf("blob path changed after failed verification: %q, err=%v", path, err)
	}
	if err := os.WriteFile(destination, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RelocateBlobs(ctx, newRoot); err != nil {
		t.Fatalf("valid pre-existing destination was rejected: %v", err)
	}
	path, err = store.BlobPath(ctx, hash)
	if err != nil || path != destination {
		t.Fatalf("verified destination path = %q, err=%v", path, err)
	}
	if err := os.WriteFile(destination, []byte("damaged in place"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.RelocateBlobs(ctx, newRoot); err == nil {
		t.Fatal("corrupt indexed destination was accepted")
	}
}

func TestRelocateBlobsBoundsDecompressionToDeclaredSize(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	payload := []byte("larger than declared")
	digest := sha256.Sum256(payload)
	hash := hex.EncodeToString(digest[:])
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(payload, nil)
	encoder.Close()
	source := filepath.Join(root, "source.zst")
	if err := os.WriteFile(source, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlob(ctx, hash, source, int64(len(payload)-1), int64(len(compressed))); err != nil {
		t.Fatal(err)
	}
	if err := store.RelocateBlobs(ctx, filepath.Join(root, "new")); err == nil {
		t.Fatal("blob larger than its declared size was accepted")
	}
	path, err := store.BlobPath(ctx, hash)
	if err != nil || path != source {
		t.Fatalf("path switched after size verification failed: %q, err=%v", path, err)
	}
}
