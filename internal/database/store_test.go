package database

import (
	"context"
	"database/sql"
	"errors"
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
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true, SyncEnabled: true, LastSeen: &now}
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
	if len(games) != 1 || games[0].SnapshotCount != 1 || games[0].PendingCount != 1 || games[0].StoredSize != 8 || games[0].LastBackup == nil || games[0].LastChange == nil {
		t.Fatalf("unexpected game summary: %#v", games)
	}
	snapshots, err := store.ListSnapshots(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].ID != snapshot.ID {
		t.Fatalf("unexpected snapshots: %#v", snapshots)
	}
	present, err := store.HasSnapshot(ctx, snapshot.ID)
	if err != nil || !present {
		t.Fatalf("saved snapshot was not found: present=%v err=%v", present, err)
	}
	present, err = store.HasSnapshot(ctx, "missing")
	if err != nil || present {
		t.Fatalf("missing snapshot lookup was incorrect: present=%v err=%v", present, err)
	}
	name := "Custom Name"
	enabled := false
	syncEnabled := false
	if err := store.UpdateGame(ctx, game.ID, core.GameUpdate{DisplayName: &name, Enabled: &enabled, SyncEnabled: &syncEnabled}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Game(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != name || updated.Enabled || updated.SyncEnabled {
		t.Fatalf("game update was not saved: %#v", updated)
	}
}

func TestPeriodicSyncSelectsOnlyWatchedGames(t *testing.T) {
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
	for _, game := range []core.Game{
		{ID: "watched", DisplayName: "Watched", Store: "custom", Enabled: true, SyncEnabled: true},
		{ID: "manual", DisplayName: "Manual", Store: "custom", Enabled: false, SyncEnabled: true},
		{ID: "local", DisplayName: "Local", Store: "custom", Enabled: true, SyncEnabled: false},
	} {
		if err := store.UpsertGame(ctx, game); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveSnapshot(ctx, core.Snapshot{Version: 1, ID: "snapshot-" + game.ID, GameID: game.ID, CreatedAt: time.Now(), RemoteState: "local"}); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := store.PendingSnapshotsForWatchedGames(ctx)
	if err != nil || len(pending) != 1 || pending[0].GameID != "watched" {
		t.Fatalf("periodic sync selection was not limited to watched games: snapshots=%#v err=%v", pending, err)
	}
}

func TestDiscoveryDiagnosticsCacheRoundTrip(t *testing.T) {
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
	if diagnostics, found, err := store.LoadDiagnostics(ctx); err != nil || found {
		t.Fatalf("empty cache returned data: diagnostics=%#v found=%v err=%v", diagnostics, found, err)
	}

	lastChecked := time.UnixMilli(1_700_000_000_123).UTC()
	lastRun := time.UnixMilli(1_700_000_100_456).UTC()
	updatedAt := time.UnixMilli(1_700_000_200_789).UTC()
	want := core.Diagnostics{
		Catalog: core.CatalogDiagnostics{Loaded: true, GameCount: 53_046, CachePath: "/catalog.db", LastChecked: &lastChecked},
		Discovery: core.DiscoveryDiagnostics{
			LastRun: &lastRun, SteamRoots: []string{`E:\steam`}, SteamInstalled: 8, EpicInstalled: 1,
			CatalogMatched: 4, GamesRegistered: 14, LocalSaveGames: 10, DeepScanMillis: 1_041,
			Unmatched: []string{"Example (123)"},
		},
		UpdatedAt: &updatedAt,
	}
	if err := store.SaveDiagnostics(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.LoadDiagnostics(ctx)
	if err != nil || !found {
		t.Fatalf("saved cache was not loaded: diagnostics=%#v found=%v err=%v", got, found, err)
	}
	if got.UpdatedAt == nil || !got.UpdatedAt.Equal(updatedAt) || got.Catalog.GameCount != want.Catalog.GameCount || got.Discovery.GamesRegistered != want.Discovery.GamesRegistered {
		t.Fatalf("diagnostics cache changed during round trip: got=%#v want=%#v", got, want)
	}
	if len(got.Discovery.SteamRoots) != 1 || got.Discovery.SteamRoots[0] != want.Discovery.SteamRoots[0] || len(got.Discovery.Unmatched) != 1 {
		t.Fatalf("diagnostics lists changed during round trip: %#v", got.Discovery)
	}
}

//nolint:gocognit // The linear lifecycle assertions are clearer together as one storage regression journey.
func TestHiddenGameLifecyclePreservesDataAndStaysOutOfSync(t *testing.T) {
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
	game := core.Game{ID: "game-hidden", DisplayName: "Hidden Game", Store: "custom", Enabled: true, SyncEnabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	if err := store.AddPath(ctx, core.GamePath{ID: "path-hidden", GameID: game.ID, Source: "custom", Template: "/saves", Resolved: "/saves", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateGamePolicy(ctx, game.ID, core.BackupPolicy{QuietSeconds: 2, MinGapSeconds: 3, MaxDirtySeconds: 4}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveSnapshot(ctx, core.Snapshot{Version: 1, ID: "snapshot-hidden", GameID: game.ID, CreatedAt: time.Now(), RemoteState: "local"}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetGameHidden(ctx, game.ID, true); err != nil {
		t.Fatal(err)
	}
	games, err := store.ListGames(ctx)
	if err != nil || len(games) != 0 {
		t.Fatalf("hidden game remained active: games=%#v err=%v", games, err)
	}
	hidden, err := store.ListHiddenGames(ctx)
	if err != nil || len(hidden) != 1 || hidden[0].PendingCount != 1 {
		t.Fatalf("hidden game summary missing: games=%#v err=%v", hidden, err)
	}
	pending, err := store.PendingSnapshots(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("hidden game leaked into global sync: snapshots=%#v err=%v", pending, err)
	}
	paths, err := store.GamePaths(ctx, game.ID)
	if err != nil || len(paths) != 1 {
		t.Fatalf("hidden game paths were lost: paths=%#v err=%v", paths, err)
	}
	policy, err := store.GamePolicy(ctx, game.ID)
	if err != nil || policy.QuietSeconds != 2 {
		t.Fatalf("hidden game policy was lost: policy=%#v err=%v", policy, err)
	}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Game(ctx, game.ID)
	if err != nil || !loaded.Hidden {
		t.Fatalf("discovery resurrected hidden game: game=%#v err=%v", loaded, err)
	}
	if err := store.SetGameHidden(ctx, game.ID, false); err != nil {
		t.Fatal(err)
	}
	games, err = store.ListGames(ctx)
	if err != nil || len(games) != 1 {
		t.Fatalf("restored game did not return: games=%#v err=%v", games, err)
	}
}

func TestMissingGameUsesDomainError(t *testing.T) {
	t.Parallel()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := store.Game(context.Background(), "missing"); !errors.Is(err, core.ErrGameNotFound) {
		t.Fatalf("expected domain not-found error, got %v", err)
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
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true, SyncEnabled: true}
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

func TestPendingSyncExcludesLocalOnlyGames(t *testing.T) {
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
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true, SyncEnabled: false}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	snapshot := core.Snapshot{Version: 1, ID: "snapshot-a", GameID: game.ID, CreatedAt: time.Now(), RemoteState: "local"}
	if err := store.SaveSnapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	pending, err := store.PendingSnapshots(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("local-only game leaked into global sync queue: snapshots=%#v err=%v", pending, err)
	}
	pending, err = store.PendingSnapshotsForGame(ctx, game.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("local-only pending snapshot was not retained: snapshots=%#v err=%v", pending, err)
	}
}

func TestOpenAddsSyncSettingToExistingDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `CREATE TABLE games (
		id TEXT PRIMARY KEY, catalog_id TEXT NOT NULL DEFAULT '', catalog_name TEXT NOT NULL DEFAULT '',
		display_name TEXT NOT NULL, store TEXT NOT NULL, store_id TEXT NOT NULL DEFAULT '', install_path TEXT NOT NULL DEFAULT '',
		image TEXT NOT NULL DEFAULT '', notes TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
		last_seen INTEGER, last_change INTEGER, last_backup INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	game := core.Game{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true, SyncEnabled: true}
	if err := store.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Game(ctx, game.ID)
	if err != nil || !loaded.SyncEnabled {
		t.Fatalf("sync setting was not added to existing database: game=%#v err=%v", loaded, err)
	}
}
