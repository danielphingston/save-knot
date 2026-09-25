//go:build linux

package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRelocationTreatsCaseDistinctLinuxRootsAsDifferentPaths(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	cleanupStoreOnTestExit(t, store)
	hash, seedPath, _ := relocationRetryFixture(t, ctx, store, root)
	blob, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(root, "Backup", hash[:2], hash+".zst")
	newRoot := filepath.Join(root, "backup")
	newPath := filepath.Join(newRoot, hash[:2], hash+".zst")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlob(ctx, hash, oldPath, int64(len("verified source for cleanup retry")), int64(len(blob))); err != nil {
		t.Fatal(err)
	}
	if sameBlobPath(oldPath, newPath) {
		t.Fatal("case-distinct Linux paths were treated as equal")
	}
	if sameFile(oldPath, newPath) {
		t.Fatal("case-distinct Linux directories unexpectedly resolve to the same file")
	}
	receipt, err := store.PrepareBlobRelocation(ctx, newRoot)
	if err != nil {
		t.Fatalf("prepare relocation from Backup to backup: %v", err)
	}
	if err := receipt.Switch(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := receipt.CleanupSources(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("case-distinct old source remains: %v", err)
	}
	assertPathExists(t, newPath, "case-distinct destination is missing")
	assertBlobPath(t, ctx, store, hash, newPath, "relocation indexed the wrong root")
	assertPendingBlobSourceCount(t, ctx, store, 0)
}
