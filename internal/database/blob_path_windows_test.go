//go:build windows

package database

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWindowsBlobPathComparisonFoldsUnicodeCase(t *testing.T) {
	root := t.TempDir()
	upper := filepath.Join(root, "Backup", "Ärchive", "blob.zst")
	lower := filepath.Join(root, "backup", "ärCHIVE", "BLOB.ZST")
	if !sameBlobPath(upper, lower) {
		t.Fatalf("Windows path comparison did not fold Unicode/case variants: %q != %q", upper, lower)
	}
}

func TestRelocationCleanupUsesUnicodeCaseFoldForIndexedPathReferences(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := Open(ctx, filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	hash, originalPath, destination := relocationRetryFixture(t, ctx, store, root)
	blob, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	unicodeSource := filepath.Join(root, "Ä-old", filepath.Base(originalPath))
	if err := os.MkdirAll(filepath.Dir(unicodeSource), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unicodeSource, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	originalSize := int64(len("verified source for cleanup retry"))
	if err := store.SaveBlob(ctx, hash, unicodeSource, originalSize, int64(len(blob))); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.PrepareBlobRelocation(ctx, filepath.Join(root, "new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Switch(ctx, true); err != nil {
		t.Fatal(err)
	}
	caseVariant := filepath.Join(root, "ä-old", filepath.Base(originalPath))
	if err := store.SaveBlob(ctx, "another-hash", caseVariant, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := receipt.CleanupSources(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathExists(t, unicodeSource, "Unicode case-folded indexed reference was deleted")
	assertPendingBlobSourceCount(t, ctx, store, 1)

	if err := store.SaveBlob(ctx, "another-hash", filepath.Join(root, "elsewhere", "another-hash.zst"), 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, unicodeSource, "source remains after Unicode case-folded reference moved")
	assertPendingBlobSourceCount(t, ctx, store, 0)
	assertBlobPath(t, ctx, store, hash, destination, "replacement path changed")
}
