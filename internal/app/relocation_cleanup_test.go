package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/saveknot/saveknot/internal/config"
)

func TestConfigureLocalRemovesOldBlobOnlyAfterSuccessfulSwitch(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	ctx := context.Background()
	game, _ := contractGame(t, application, paths.Root, "move-and-clean")
	snapshot, err := application.coordinator.Backup(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := snapshot.Files[0].Hash
	oldPath, err := application.database.BlobPath(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(paths.Root, "moved-blobs")
	if err := application.coordinator.ConfigureLocal(ctx, config.Local{LocalBackupDir: newRoot}); err != nil {
		t.Fatal(err)
	}
	newPath := filepath.Join(newRoot, hash[:2], hash+".zst")
	if got, err := application.database.BlobPath(ctx, hash); err != nil || got != newPath {
		t.Fatalf("indexed path after successful switch = %q, err=%v; want %q", got, err, newPath)
	}
	if _, err := os.Stat(newPath); err != nil {
		t.Fatalf("verified destination missing: %v", err)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old blob remains after successful switch: %v", err)
	}
}

func TestConfigureLocalFailedSettingsPreservesOldBlobAndRemovesAttemptedCopy(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	ctx := context.Background()
	game, _ := contractGame(t, application, paths.Root, "failed-move-cleanup")
	snapshot, err := application.coordinator.Backup(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := snapshot.Files[0].Hash
	oldPath, err := application.database.BlobPath(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := application.coordinator.snapshots.BlobRoot()
	failedRoot := filepath.Join(paths.Root, "failed-move")
	if err := application.coordinator.ConfigureLocal(ctx, config.Local{
		LocalBackupDir: failedRoot, SteamRoots: []string{"relative-path"},
	}); err == nil {
		t.Fatal("invalid settings unexpectedly saved")
	}
	if got := application.coordinator.snapshots.BlobRoot(); got != oldRoot {
		t.Fatalf("active root after failed settings = %q; want %q", got, oldRoot)
	}
	if got := application.coordinator.settings.Config().LocalBackupDir; got != oldRoot {
		t.Fatalf("saved root after failed settings = %q; want %q", got, oldRoot)
	}
	if got, err := application.database.BlobPath(ctx, hash); err != nil || got != oldPath {
		t.Fatalf("indexed path after failed settings = %q, err=%v; want %q", got, err, oldPath)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("failed settings lost original blob: %v", err)
	}
	copyPath := filepath.Join(failedRoot, hash[:2], hash+".zst")
	if _, err := os.Stat(copyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempted destination copy remains after rollback: %v", err)
	}
}

func TestConfigureLocalFailedSettingsPreservesPreexistingDestinationAndSamePath(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	ctx := context.Background()
	game, _ := contractGame(t, application, paths.Root, "shared-move")
	snapshot, err := application.coordinator.Backup(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := snapshot.Files[0].Hash
	oldPath, err := application.database.BlobPath(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	oldData, err := os.ReadFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	sharedRoot := filepath.Join(paths.Root, "shared-destination")
	sharedPath := filepath.Join(sharedRoot, hash[:2], hash+".zst")
	if err := os.MkdirAll(filepath.Dir(sharedPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sharedPath, oldData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.coordinator.ConfigureLocal(ctx, config.Local{
		LocalBackupDir: sharedRoot, SteamRoots: []string{"relative-path"},
	}); err == nil {
		t.Fatal("invalid settings unexpectedly saved")
	}
	for _, path := range []string{oldPath, sharedPath} {
		if data, err := os.ReadFile(path); err != nil || string(data) != string(oldData) {
			t.Fatalf("preexisting blob %q changed after failed settings: err=%v", path, err)
		}
	}
	if got, err := application.database.BlobPath(ctx, hash); err != nil || got != oldPath {
		t.Fatalf("shared destination changed indexed path to %q, err=%v", got, err)
	}
	if err := application.coordinator.ConfigureLocal(ctx, config.Local{LocalBackupDir: application.coordinator.snapshots.BlobRoot()}); err != nil {
		t.Fatalf("same-path configuration failed: %v", err)
	}
	if data, err := os.ReadFile(oldPath); err != nil || string(data) != string(oldData) {
		t.Fatalf("same-path configuration changed indexed blob: err=%v", err)
	}
}
