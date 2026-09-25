package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/config"
)

//nolint:gocognit // Covers the full canceled relocation and rollback sequence.
func TestConfigureLocalSettingsFailureRollsBackAfterCancellation(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	game, _ := contractGame(t, application, paths.Root, "cancelled-relocation")
	snapshot, err := application.coordinator.Backup(context.Background(), game.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := snapshot.Files[0].Hash
	oldPath, err := application.database.BlobPath(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := application.coordinator.snapshots.BlobRoot()
	newRoot := filepath.Join(paths.Root, "cancelled-relocation-blobs")
	newPath := filepath.Join(newRoot, hash[:2], hash+".zst")

	ctx, cancel := context.WithCancel(context.Background())
	cancelObserved := make(chan struct{})
	go func() {
		defer close(cancelObserved)
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				indexed, err := application.database.BlobPath(context.WithoutCancel(ctx), hash)
				if err == nil && filepath.Clean(indexed) == filepath.Clean(newPath) {
					cancel()
					return
				}
			}
		}
	}()

	steamRoots := make([]string, 0, 100_001)
	for i := 0; i < 100_000; i++ {
		steamRoots = append(steamRoots, filepath.Join(paths.Root, fmt.Sprintf("steam-%d", i)))
	}
	steamRoots = append(steamRoots, "relative-root")
	err = application.coordinator.ConfigureLocal(ctx, config.Local{LocalBackupDir: newRoot, SteamRoots: steamRoots})
	<-cancelObserved
	if err == nil {
		t.Fatal("invalid settings unexpectedly saved")
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("request context error = %v; want canceled", ctx.Err())
	}
	if got := application.coordinator.snapshots.BlobRoot(); got != oldRoot {
		t.Fatalf("active root after canceled rollback = %q; want %q", got, oldRoot)
	}
	if got := application.coordinator.settings.Config().LocalBackupDir; got != oldRoot {
		t.Fatalf("saved root after canceled rollback = %q; want %q", got, oldRoot)
	}
	if got, err := application.database.BlobPath(context.Background(), hash); err != nil || got != oldPath {
		t.Fatalf("indexed path after canceled rollback = %q, err=%v; want %q", got, err, oldPath)
	}
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatalf("source blob lost after canceled rollback: %v", err)
	}
	if _, err := os.Stat(newPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("attempt destination remains after canceled rollback: %v", err)
	}
}
