package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
)

func contractGame(t *testing.T, application *Application, root, id string) (core.Game, string) {
	t.Helper()
	ctx := context.Background()
	game := core.Game{ID: id, DisplayName: id, Store: "custom", Enabled: true, SyncEnabled: true}
	if err := application.database.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	save := filepath.Join(root, id+".sav")
	if err := os.WriteFile(save, []byte(id+" first save"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := application.database.AddPath(ctx, core.GamePath{ID: id + "-path", GameID: id, Source: "custom", Template: save, Resolved: save, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return game, save
}

func TestConfigureLocalRelocationPreservesWriterLayoutAndRollsBackOnSettingsError(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	ctx := context.Background()
	game, save := contractGame(t, application, paths.Root, "relocation")
	first, err := application.coordinator.Backup(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	hash := first.Files[0].Hash
	newRoot := filepath.Join(paths.Root, "new-backups")
	if err := application.coordinator.ConfigureLocal(ctx, config.Local{LocalBackupDir: newRoot}); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(newRoot, hash[:2], hash+".zst")
	if got, err := application.database.BlobPath(ctx, hash); err != nil || got != want {
		t.Fatalf("relocated blob path = %q, err = %v; want %q", got, err, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("relocated blob is missing: %v", err)
	}
	if err := os.WriteFile(save, []byte("relocation second save"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := application.coordinator.Backup(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondHash := second.Files[0].Hash
	secondWant := filepath.Join(newRoot, secondHash[:2], secondHash+".zst")
	if got, err := application.database.BlobPath(ctx, secondHash); err != nil || got != secondWant {
		t.Fatalf("new backup used a different blob layout: path=%q err=%v want=%q", got, err, secondWant)
	}
	failedRoot := filepath.Join(paths.Root, "failed-backups")
	if err := application.coordinator.ConfigureLocal(ctx, config.Local{
		LocalBackupDir: failedRoot, SteamRoots: []string{"relative-path"},
	}); err == nil {
		t.Fatal("invalid local settings unexpectedly saved")
	}
	if got := application.coordinator.snapshots.BlobRoot(); got != newRoot {
		t.Fatalf("failed relocation changed active blob root to %q", got)
	}
	if got := application.coordinator.settings.Config().LocalBackupDir; got != newRoot {
		t.Fatalf("failed relocation changed saved blob root to %q", got)
	}
	for _, item := range []struct{ hash, path string }{{hash, want}, {secondHash, secondWant}} {
		if got, err := application.database.BlobPath(ctx, item.hash); err != nil || got != item.path {
			t.Fatalf("failed relocation changed indexed path to %q, err=%v; want %q", got, err, item.path)
		}
		if _, err := os.Stat(item.path); err != nil {
			t.Fatalf("failed relocation lost indexed blob: %v", err)
		}
	}
}

func TestBackupCompletesWhileRemoteMutationLockIsHeld(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	game, _ := contractGame(t, application, paths.Root, "manual")
	done := make(chan error, 1)
	application.coordinator.syncMu.Lock()
	go func() {
		_, err := application.coordinator.Backup(context.Background(), game.ID)
		done <- err
	}()
	select {
	case err := <-done:
		application.coordinator.syncMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		application.coordinator.syncMu.Unlock()
		<-done
		t.Fatal("local backup waited for remote mutation lock")
	}
}

func TestWatcherBackupCompletesWhileRemoteMutationLockIsHeld(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	game, save := contractGame(t, application, paths.Root, "watched")
	done := make(chan struct{})
	application.coordinator.syncMu.Lock()
	go func() {
		application.coordinator.automaticBackup(context.Background(), game.ID)
		close(done)
	}()
	select {
	case <-done:
		application.coordinator.syncMu.Unlock()
	case <-time.After(2 * time.Second):
		application.coordinator.syncMu.Unlock()
		<-done
		t.Fatal("watcher worker waited for remote mutation lock")
	}
	first, err := application.database.LatestSnapshot(context.Background(), game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(save, []byte("watched second save"), 0o600); err != nil {
		t.Fatal(err)
	}
	application.coordinator.automaticBackup(context.Background(), game.ID)
	second, err := application.database.LatestSnapshot(context.Background(), game.ID)
	if err != nil || second.ID == first.ID {
		t.Fatalf("watcher did not continue backing up: snapshot=%#v err=%v", second, err)
	}
}

func TestWatcherBackupsDoNotWaitWhenAutomaticSyncQueueIsSaturated(t *testing.T) {
	application, paths := buildBackupTestApplication(t)
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	application.coordinator.syncMu.Lock()
	locked := true
	go func() {
		application.coordinator.runAutomaticSync(workerCtx, workerDone)
	}()
	t.Cleanup(func() {
		if locked {
			application.coordinator.syncMu.Unlock()
		}
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(2 * time.Second):
			t.Error("automatic sync worker did not stop")
		}
	})

	firstGame, _ := contractGame(t, application, paths.Root, "queued-first")
	application.coordinator.automaticBackup(context.Background(), firstGame.ID)
	deadline := time.Now().Add(2 * time.Second)
	for {
		application.coordinator.automaticSyncMu.Lock()
		queued := len(application.coordinator.automaticSyncGames)
		application.coordinator.automaticSyncMu.Unlock()
		if queued == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic sync worker did not take the first game")
		}
		time.Sleep(time.Millisecond)
	}

	const additionalBackups = 40
	for index := 0; index < additionalBackups; index++ {
		gameID := fmt.Sprintf("queued-%02d", index)
		game, _ := contractGame(t, application, paths.Root, gameID)
		completed := make(chan struct{})
		go func() {
			application.coordinator.automaticBackup(context.Background(), game.ID)
			close(completed)
		}()
		select {
		case <-completed:
		case <-time.After(2 * time.Second):
			t.Fatalf("watcher backup %q waited for remote queue capacity", game.ID)
		}
		if _, err := application.database.LatestSnapshot(context.Background(), game.ID); err != nil {
			t.Fatalf("watcher backup %q did not persist locally: %v", game.ID, err)
		}
	}
	application.coordinator.syncMu.Unlock()
	locked = false
}
