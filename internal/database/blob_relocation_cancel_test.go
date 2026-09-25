package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestPrepareBlobRelocationCancellationRemovesCreatedDestination(t *testing.T) {
	root := t.TempDir()
	store, err := Open(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	cleanupStoreOnTestExit(t, store)

	payload := make([]byte, 64<<20)
	if _, err := rand.New(rand.NewSource(73)).Read(payload); err != nil {
		t.Fatal(err)
	}
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
	if err := store.SaveBlob(context.Background(), hash, oldPath, int64(len(payload)), int64(len(compressed))); err != nil {
		t.Fatal(err)
	}

	newRoot := filepath.Join(root, "new")
	destination := filepath.Join(newRoot, hash[:2], hash+".zst")
	ctx, cancel := context.WithCancel(context.Background())
	cancelObserved := make(chan struct{})
	go func() {
		defer close(cancelObserved)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			if _, err := os.Stat(destination); err == nil {
				cancel()
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	_, err = store.PrepareBlobRelocation(ctx, newRoot)
	cancel()
	<-cancelObserved
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare error = %v; want context.Canceled", err)
	}
	if got, err := store.BlobPath(context.Background(), hash); err != nil || got != oldPath {
		t.Fatalf("index after canceled prepare = %q, err=%v; want source %q", got, err, oldPath)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("source blob lost after canceled prepare: %v", err)
	}
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created destination remains after canceled prepare: %v", err)
	}
}
