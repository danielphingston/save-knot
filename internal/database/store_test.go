package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

func TestStoreGamePathsAndSnapshots(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	now := time.Unix(1_700_000_000, 0).UTC()
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true, LastSeen: &now}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	path := core.GamePath{ID: "path-a", GameID: game.ID, Source: "custom", Template: "/saves", Resolved: "/saves", Enabled: true}
	if err := store.AddPath(ctx, path); err != nil {
		t.Fatal(err)
	}
	snapshot := core.Snapshot{Version: 1, ID: "snapshot-a", GameID: game.ID, GameName: game.DisplayName, DeviceID: "device-a", CreatedAt: now, Files: []core.SnapshotFile{{Path: "save.dat", Hash: "abc", Size: 10}}, OriginalSize: 10, StoredSize: 8, RemoteState: "local"}
	if err := store.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	games, err := store.ListGames(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].SnapshotCount != 1 || games[0].StoredSize != 8 || games[0].LastBackup == nil {
		t.Fatalf("unexpected game summary: %#v", games)
	}
	snapshots, err := store.ListSnapshots(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != snapshot.ID {
		t.Fatalf("unexpected snapshots: %#v", snapshots)
	}
	name := "Custom Name"
	enabled := false
	if err := store.UpdateGame(ctx, game.ID, core.GameUpdate{DisplayName: &name, Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Game(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != name || updated.Enabled {
		t.Fatalf("game update was not saved: %#v", updated)
	}
}

func TestRemoteStateIsScopedToStorageTarget(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	snapshot := core.Snapshot{Version: 1, ID: "snapshot-a", GameID: game.ID, CreatedAt: time.Now(), RemoteState: "local"}
	if err := store.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingSnapshots(ctx)
	if err != nil || len(pending) != 1 {
		t.Fatalf("unexpected pending snapshots: snapshots=%#v err=%v", pending, err)
	}
	if err := store.SaveBlob(ctx, "abc", "/blobs/abc.zst", 10, 8); err != nil {
		t.Fatal(err)
	}
	uploaded, err := store.BlobUploaded(ctx, "abc", "account/bucket/one")
	if err != nil || uploaded {
		t.Fatalf("new blob has incorrect upload state: uploaded=%v err=%v", uploaded, err)
	}
	if err := store.MarkBlobUploaded(ctx, "abc", "account/bucket/one"); err != nil {
		t.Fatal(err)
	}
	uploaded, err = store.BlobUploaded(ctx, "abc", "account/bucket/one")
	if err != nil || !uploaded {
		t.Fatalf("blob upload state was not saved: uploaded=%v err=%v", uploaded, err)
	}
	uploaded, err = store.BlobUploaded(ctx, "abc", "account/bucket/two")
	if err != nil || uploaded {
		t.Fatalf("blob upload leaked between R2 targets: uploaded=%v err=%v", uploaded, err)
	}
	if err := store.MarkSnapshotRemote(ctx, snapshot.ID, "synced"); err != nil {
		t.Fatal(err)
	}
	pending, err = store.PendingSnapshots(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("synced snapshot remained pending: snapshots=%#v err=%v", pending, err)
	}
}
