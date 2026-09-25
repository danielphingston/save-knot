package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// A nonempty directory makes os.Remove fail on every supported platform. This
// models a transient source-removal failure after the destination is indexed.
func TestRelocationCleanupRetriesAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "state.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	hash, oldPath, destination, receipt := prepareRetryableRelocation(t, ctx, store, root)
	savedSource, blocker := makeSourceUndeletable(t, root, oldPath)
	requireCleanupFailure(t, ctx, receipt)
	assertBlobPath(t, ctx, store, hash, destination, "failed cleanup changed the active blob path")
	assertPathExists(t, destination, "failed cleanup lost the active destination")
	restoreSource(t, oldPath, savedSource, blocker)

	store, err = reopenRelocationRetryStore(t, ctx, store, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatalf("retry after reopening the database: %v", err)
	}
	assertPathMissing(t, oldPath, "obsolete source remains after retry")

	// Once a queued deletion succeeds, a later file at that path is unrelated.
	if err := os.WriteFile(oldPath, []byte("new unrelated file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathExists(t, oldPath, "completed cleanup retained a stale pending deletion")
}

func TestRelocationCleanupFollowsTwoCommittedMovesAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "state.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	hash, aPath, bPath, first := prepareRetryableRelocation(t, ctx, store, root)
	savedSource, blocker := makeSourceUndeletable(t, root, aPath)
	requireCleanupFailure(t, ctx, first)
	assertPendingBlobSourceCount(t, ctx, store, 1)
	restoreSource(t, aPath, savedSource, blocker)

	cRoot := filepath.Join(root, "third")
	cPath := filepath.Join(cRoot, hash[:2], hash+".zst")
	second, err := store.PrepareBlobRelocation(ctx, cRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Switch(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := second.CleanupSources(ctx); err != nil {
		t.Fatalf("clean up second committed move: %v", err)
	}
	assertBlobPath(t, ctx, store, hash, cPath, "second move changed the active index")
	assertPathExists(t, cPath, "second move lost the active blob")

	store, err = reopenRelocationRetryStore(t, ctx, store, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx, cRoot); err != nil {
		t.Fatalf("retry all obsolete sources for active root after restart: %v", err)
	}
	assertPathMissing(t, aPath, "first obsolete source remains after chained relocation")
	assertPathMissing(t, bPath, "second obsolete source remains after chained relocation")
	assertPathExists(t, cPath, "cleanup removed the current blob")
	assertBlobPath(t, ctx, store, hash, cPath, "cleanup changed the active index")
	assertPendingBlobSourceCount(t, ctx, store, 0)
}

func TestChainedRelocationRollbackRestoresOlderPendingSource(t *testing.T) {
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
	hash, aPath, bPath, first := prepareRetryableRelocation(t, ctx, store, root)
	savedSource, blocker := makeSourceUndeletable(t, root, aPath)
	requireCleanupFailure(t, ctx, first)
	restoreSource(t, aPath, savedSource, blocker)

	cRoot := filepath.Join(root, "third")
	second, err := store.PrepareBlobRelocation(ctx, cRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Switch(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := second.Rollback(ctx); err != nil {
		t.Fatalf("rollback second move: %v", err)
	}
	assertBlobPath(t, ctx, store, hash, bPath, "rollback did not restore the active path")
	assertPathExists(t, bPath, "rollback removed the active source")
	assertPendingBlobSourceCount(t, ctx, store, 1)
	var source, target, replacement string
	if err := store.db.QueryRowContext(ctx, `SELECT source_path, target_root, replacement_path FROM pending_blob_sources WHERE hash = ?`, hash).Scan(&source, &target, &replacement); err != nil {
		t.Fatal(err)
	}
	if source != aPath || target != filepath.Join(root, "new") || replacement != bPath {
		t.Fatalf("pending source after rollback = (%q, %q, %q), want (%q, %q, %q)", source, target, replacement, aPath, filepath.Join(root, "new"), bPath)
	}
}

func TestRelocationCleanupRetriesReusedIndexedPathAfterReferenceMoves(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "state.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	_, oldPath, _, receipt := prepareRetryableRelocation(t, ctx, store, root)
	savedSource, blocker := makeSourceUndeletable(t, root, oldPath)
	requireCleanupFailure(t, ctx, receipt)
	restoreSource(t, oldPath, savedSource, blocker)
	if err := store.SaveBlob(ctx, "another-hash", oldPath, 1, 1); err != nil {
		t.Fatal(err)
	}

	store, err = reopenRelocationRetryStore(t, ctx, store, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathExists(t, oldPath, "retry deleted an indexed, reused source path")
	assertPendingBlobSourceCount(t, ctx, store, 1)
	assertBlobPath(t, ctx, store, "another-hash", oldPath, "reused path changed")

	movedReference := filepath.Join(root, "elsewhere", "another-hash.zst")
	if err := store.SaveBlob(ctx, "another-hash", movedReference, 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, oldPath, "source remains after reused reference moved")
	assertPendingBlobSourceCount(t, ctx, store, 0)
}

func TestRelocationCleanupPreservesSymlinkAliasToReplacement(t *testing.T) {
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
	_, oldPath, destination, receipt := prepareRetryableRelocation(t, ctx, store, root)
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(destination, oldPath); err != nil {
		t.Skipf("symlink creation is unavailable on this system: %v", err)
	}
	if err := receipt.CleanupSources(ctx); err != nil {
		t.Fatal(err)
	}
	linkInfo, err := os.Lstat(oldPath)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("source alias was removed: mode=%v, err=%v", linkInfo, err)
	}
	assertPathExists(t, destination, "cleanup removed the indexed replacement")
	assertPendingBlobSourceCount(t, ctx, store, 0)
}

func TestRelocationFinalizationFallsBackToNoReplaceRename(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "staged.zst")
	destination := filepath.Join(root, "final.zst")
	payload := []byte("verified staged blob")
	if err := os.WriteFile(staging, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	journaled := false
	usedRename, err := finalizeStagedBlob(staging, destination,
		func(string, string) error { return errors.New("simulated unsupported hard link") }, renameNoReplace,
		func() error { journaled = true; return nil })
	if err != nil || !usedRename || !journaled {
		t.Fatalf("fallback finalization = (usedRename %v, err %v, journaled %v)", usedRename, err, journaled)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("renamed destination = %q, err=%v", got, err)
	}
	assertPathMissing(t, staging, "successful fallback retained its staging file")

	preexisting := []byte("preexisting destination")
	if err := os.WriteFile(staging, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, preexisting, 0o600); err != nil {
		t.Fatal(err)
	}
	usedRename, err = finalizeStagedBlob(staging, destination,
		func(string, string) error { return errors.New("simulated unsupported hard link") }, renameNoReplace,
		func() error { return nil })
	if !usedRename || !errors.Is(err, os.ErrExist) {
		t.Fatalf("fallback over preexisting destination = (usedRename %v, err %v); want existing-path error", usedRename, err)
	}
	got, err = os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, preexisting) {
		t.Fatalf("fallback changed preexisting destination: data=%q, err=%v", got, err)
	}
}

func TestRelocationPreparationArtifactsRecoverAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "state.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	hash, oldPath, destination := relocationRetryFixture(t, ctx, store, root)
	if _, err := store.PrepareBlobRelocation(ctx, filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}
	assertPathExists(t, destination, "prepare did not create replacement destination")
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, destination, "abandoned attempt-created destination remains")
	assertBlobPath(t, ctx, store, hash, oldPath, "interrupted prepare changed the indexed source")
}

func TestRelocationPreparationRenameArtifactRecoversAfterRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "state.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	hash, oldPath, destination := relocationRetryFixture(t, ctx, store, root)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	stagingPath := destination + ".prepare-fallback-attempt"
	if err := os.WriteFile(stagingPath, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO relocation_artifacts
		(artifact_id, hash, source_path, target_root, destination_path, staging_path, original_size, staging_owned, finalized_by_rename, state)
		VALUES ('fallback-attempt', ?, ?, ?, ?, ?, ?, 1, 1, 'preparing')`,
		hash, oldPath, filepath.Dir(filepath.Dir(destination)), destination, stagingPath, int64(len("verified source for cleanup retry"))); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(stagingPath, destination); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, destination, "abandoned no-replace rename destination remains")
	assertBlobPath(t, ctx, store, hash, oldPath, "interrupted rename changed the indexed source")
}

func TestRelocationPreparationRecoveryPreservesPreexistingDestination(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "state.db")
	store, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	hash, oldPath, destination := relocationRetryFixture(t, ctx, store, root)
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PrepareBlobRelocation(ctx, filepath.Join(root, "new")); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CleanupPendingBlobSources(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(got, blob) {
		t.Fatalf("preexisting destination changed or disappeared: err=%v", err)
	}
	assertBlobPath(t, ctx, store, hash, oldPath, "interrupted prepare changed the indexed source")
}

func assertPendingBlobSourceCount(t *testing.T, ctx context.Context, store *Store, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pending_blob_sources`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("pending obsolete source count = %d; want %d", got, want)
	}
}

func prepareRetryableRelocation(t *testing.T, ctx context.Context, store *Store, root string) (hash, oldPath, destination string, receipt *BlobRelocation) {
	t.Helper()
	hash, oldPath, destination = relocationRetryFixture(t, ctx, store, root)
	var err error
	receipt, err = store.PrepareBlobRelocation(ctx, filepath.Join(root, "new"))
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Switch(ctx, true); err != nil {
		t.Fatal(err)
	}
	return hash, oldPath, destination, receipt
}

func makeSourceUndeletable(t *testing.T, root, oldPath string) (savedSource, blocker string) {
	t.Helper()
	savedSource = filepath.Join(root, "saved-source.zst")
	if err := os.Rename(oldPath, savedSource); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(oldPath, 0o700); err != nil {
		t.Fatal(err)
	}
	blocker = filepath.Join(oldPath, "held-open")
	if err := os.WriteFile(blocker, []byte("simulate an in-use source"), 0o600); err != nil {
		t.Fatal(err)
	}
	return savedSource, blocker
}

func requireCleanupFailure(t *testing.T, ctx context.Context, receipt *BlobRelocation) {
	t.Helper()
	if err := receipt.CleanupSources(ctx); err == nil {
		t.Fatal("obsolete source removal unexpectedly succeeded")
	}
}

func restoreSource(t *testing.T, oldPath, savedSource, blocker string) {
	t.Helper()
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(savedSource, oldPath); err != nil {
		t.Fatal(err)
	}
}

func reopenRelocationRetryStore(t *testing.T, ctx context.Context, store *Store, dbPath string) (*Store, error) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return Open(ctx, dbPath)
}

func assertBlobPath(t *testing.T, ctx context.Context, store *Store, hash, want, message string) {
	t.Helper()
	got, err := store.BlobPath(ctx, hash)
	if err != nil || got != want {
		t.Fatalf("%s: path = %q, err=%v; want %q", message, got, err, want)
	}
}

func assertPathExists(t *testing.T, path, message string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s: %v", message, err)
	}
}

func assertPathMissing(t *testing.T, path, message string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s: %v", message, err)
	}
}

func relocationRetryFixture(t *testing.T, ctx context.Context, store *Store, root string) (hash, oldPath, destination string) {
	t.Helper()
	payload := []byte("verified source for cleanup retry")
	digest := sha256.Sum256(payload)
	hash = hex.EncodeToString(digest[:])
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(payload, nil)
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	oldPath = filepath.Join(root, "old", hash[:2], hash+".zst")
	destination = filepath.Join(root, "new", hash[:2], hash+".zst")
	if err := os.MkdirAll(filepath.Dir(oldPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, compressed, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveBlob(ctx, hash, oldPath, int64(len(payload)), int64(len(compressed))); err != nil {
		t.Fatal(err)
	}
	return hash, oldPath, destination
}
