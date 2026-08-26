package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/catalog"
	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/database"
	"github.com/saveknot/saveknot/internal/snapshot"
)

func TestApplicationBuildAndCatalogDiscovery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	paths := config.DataPaths(dataDir)
	cfg := config.Config{
		Listen: "127.0.0.1:0", ManifestURL: config.DefaultManifestURL, LocalBackupDir: filepath.Join(dataDir, "blobs"),
		RetentionKeep: 50, DeviceID: "test-device", R2: config.R2{Prefix: "saveknot"},
	}
	if err := config.Save(paths.Config, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Catalog, []byte("Example Game:\n  files:\n    <home>/.saveknot-test-missing: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := catalog.CompileFile(ctx, paths.Catalog, paths.CatalogDB); err != nil {
		t.Fatal(err)
	}
	application, err := Build(ctx, dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(application.coordinator.Close(), application.database.Close()); err != nil {
			t.Error(err)
		}
	})

	application.coordinator.discover(ctx)
	diagnostics := application.coordinator.Diagnostics()
	if !diagnostics.Catalog.Loaded || diagnostics.Catalog.GameCount != 1 {
		t.Fatalf("catalog did not become ready: %#v", diagnostics)
	}
}

func TestBuildNormalizesRelativeDataAndBackupPaths(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	dataDir := "state"
	paths := config.DataPaths(dataDir)
	if err := config.Save(paths.Config, config.Config{
		Listen: "127.0.0.1:0", LocalBackupDir: filepath.Join(dataDir, "blobs"),
		RetentionKeep: 50, DeviceID: "test-device", R2: config.R2{Prefix: "saveknot"},
	}); err != nil {
		t.Fatal(err)
	}
	application, err := Build(context.Background(), dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(application.coordinator.Close(), application.database.Close()); err != nil {
			t.Error(err)
		}
	})
	backupDir := application.coordinator.settings.Config().LocalBackupDir
	if !filepath.IsAbs(backupDir) || backupDir != filepath.Join(root, dataDir, "blobs") {
		t.Fatalf("relative backup path was not normalized: %q", backupDir)
	}
	game := core.Game{ID: "hidden-game", DisplayName: "Hidden Game", Store: "custom", Enabled: true, SyncEnabled: true}
	if err := application.database.UpsertGame(context.Background(), game); err != nil {
		t.Fatal(err)
	}
	if err := application.coordinator.SetGameHidden(context.Background(), game.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := application.coordinator.Backup(context.Background(), game.ID); !errors.Is(err, core.ErrGameNotFound) {
		t.Fatalf("hidden game accepted a queued backup: %v", err)
	}
}

func TestBuildLoadsCachedDiscoveryDiagnostics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	paths := config.DataPaths(dataDir)
	if err := config.Save(paths.Config, config.Config{
		Listen: "127.0.0.1:0", LocalBackupDir: filepath.Join(dataDir, "blobs"),
		RetentionKeep: 50, DeviceID: "test-device", R2: config.R2{Prefix: "saveknot"},
	}); err != nil {
		t.Fatal(err)
	}
	store, err := database.Open(ctx, paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	updatedAt := time.UnixMilli(1_700_000_000_123).UTC()
	lastRun := time.UnixMilli(1_700_000_100_456).UTC()
	want := core.Diagnostics{
		Discovery: core.DiscoveryDiagnostics{LastRun: &lastRun, SteamInstalled: 8, GamesRegistered: 14, SteamRoots: []string{`E:\steam`}},
		UpdatedAt: &updatedAt,
	}
	if err := store.SaveDiagnostics(ctx, want); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	application, err := Build(ctx, dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(application.coordinator.Close(), application.database.Close()); err != nil {
			t.Error(err)
		}
	})
	got := application.coordinator.Diagnostics()
	if got.UpdatedAt == nil || !got.UpdatedAt.Equal(updatedAt) || got.Discovery.LastRun == nil || !got.Discovery.LastRun.Equal(lastRun) {
		t.Fatalf("cached diagnostic timestamps were not restored: %#v", got)
	}
	if got.Discovery.SteamInstalled != want.Discovery.SteamInstalled || got.Discovery.GamesRegistered != want.Discovery.GamesRegistered || len(got.Discovery.SteamRoots) != 1 {
		t.Fatalf("cached discovery counts were not restored: %#v", got.Discovery)
	}
}

func TestRemapManualGameLinksCatalogWithoutReplacingCustomPaths(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dataDir := t.TempDir()
	paths := config.DataPaths(dataDir)
	if err := config.Save(paths.Config, config.Config{
		Listen: "127.0.0.1:0", LocalBackupDir: filepath.Join(dataDir, "blobs"),
		RetentionKeep: 50, DeviceID: "test-device", R2: config.R2{Prefix: "saveknot"},
	}); err != nil {
		t.Fatal(err)
	}
	manifest := "\"Uncharted: Legacy of Thieves Collection\":\n  files:\n    <home>/Saved Games/Uncharted Legacy of Thieves Collection/users: {}\n"
	if err := os.WriteFile(paths.Catalog, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := catalog.CompileFile(ctx, paths.Catalog, paths.CatalogDB); err != nil {
		t.Fatal(err)
	}
	application, err := Build(ctx, dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(application.coordinator.Close(), application.database.Close()); err != nil {
			t.Error(err)
		}
	})

	game := core.Game{ID: "manual-uncharted", DisplayName: "Uncharted 4", Store: "custom", Enabled: true, SyncEnabled: true}
	if err := application.database.UpsertGame(ctx, game); err != nil {
		t.Fatal(err)
	}
	customPath := core.GamePath{
		ID: "manual-path", GameID: game.ID, Source: "custom",
		Template: filepath.Join(dataDir, "users"), Resolved: filepath.Join(dataDir, "users"), Enabled: true,
	}
	if err := application.database.AddPath(ctx, customPath); err != nil {
		t.Fatal(err)
	}

	const catalogName = "Uncharted: Legacy of Thieves Collection"
	if err := application.coordinator.RemapGame(ctx, game.ID, catalogName); err != nil {
		t.Fatal(err)
	}
	linked, err := application.database.Game(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if linked.CatalogID != catalogName || linked.CatalogName != catalogName || linked.DisplayName != game.DisplayName {
		t.Fatalf("manual game was not linked without changing its display name: %#v", linked)
	}
	linkedPaths, err := application.database.GamePaths(ctx, game.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(linkedPaths) != 1 || linkedPaths[0] != customPath {
		t.Fatalf("manual catalog link changed custom paths: %#v", linkedPaths)
	}
}

func TestCoordinatorStartOnlyWatchesRegisteredGames(t *testing.T) {
	dataDir := t.TempDir()
	xdgData := filepath.Join(dataDir, "xdg-data")
	t.Setenv("XDG_DATA_HOME", xdgData)
	savePath := filepath.Join(xdgData, "Example", "save.dat")
	if err := os.MkdirAll(filepath.Dir(savePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(savePath, []byte("save"), 0o600); err != nil {
		t.Fatal(err)
	}

	manifest := "Example Game:\n  files:\n    <xdgData>/Example/save.dat: {}\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotModified)
	}))
	t.Cleanup(server.Close)
	paths := config.DataPaths(dataDir)
	cfg := config.Config{
		Listen: "127.0.0.1:0", ManifestURL: server.URL, LocalBackupDir: filepath.Join(dataDir, "blobs"),
		RetentionKeep: 50, DeviceID: "test-device", R2: config.R2{Prefix: "saveknot"},
	}
	if err := config.Save(paths.Config, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Catalog, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := catalog.CompileFile(context.Background(), paths.Catalog, paths.CatalogDB); err != nil {
		t.Fatal(err)
	}
	application, err := Build(context.Background(), dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	application.coordinator.Start(ctx)
	t.Cleanup(func() {
		cancel()
		if err := errors.Join(application.coordinator.Close(), application.database.Close()); err != nil {
			t.Error(err)
		}
	})

	deadline := time.Now().Add(time.Second)
	for !application.coordinator.Diagnostics().Catalog.Loaded && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	games, err := application.database.ListGames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 0 {
		t.Fatalf("startup discovered games without a manual scan: %#v", games)
	}

	application.coordinator.DiscoverNow(context.Background())
	games, err = application.database.ListGames(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].DisplayName != "Example Game" {
		t.Fatalf("manual scan did not register the existing save: %#v", games)
	}
}

func TestSyncSnapshotBatchContinuesAfterIndividualFailure(t *testing.T) {
	t.Parallel()
	snapshots := []core.Snapshot{{ID: "one", GameID: "game-a"}, {ID: "two", GameID: "game-b"}, {ID: "three", GameID: "game-c"}}
	var attempted []string
	var published []core.Event
	result, syncedGames, err := syncSnapshotBatch(snapshots, func(snapshot core.Snapshot) error {
		attempted = append(attempted, snapshot.ID)
		if snapshot.ID == "two" {
			return errors.New("upload failed")
		}
		return nil
	}, func(event core.Event) {
		published = append(published, event)
	})
	if err == nil || result.Eligible != 3 || result.Synced != 2 || result.Failed != 1 {
		t.Fatalf("unexpected partial result: result=%#v err=%v", result, err)
	}
	if len(attempted) != 3 || attempted[2] != "three" {
		t.Fatalf("later upload was aborted: %#v", attempted)
	}
	if len(syncedGames) != 2 || len(published) != 3 || published[1].Type != "upload.failed" {
		t.Fatalf("batch outcomes were not recorded: games=%#v events=%#v", syncedGames, published)
	}
}

func TestCheckpointWatchedGamesOnlyChecksEligibleGamesAndContinuesAfterFailure(t *testing.T) {
	t.Parallel()
	games := []core.Game{
		{ID: "changed", DisplayName: "Changed", Enabled: true, SyncEnabled: true},
		{ID: "unchanged", DisplayName: "Unchanged", Enabled: true, SyncEnabled: true},
		{ID: "empty", DisplayName: "Empty", Enabled: true, SyncEnabled: true},
		{ID: "failed", DisplayName: "Failed", Enabled: true, SyncEnabled: true},
		{ID: "manual", DisplayName: "Manual", Enabled: false, SyncEnabled: true},
		{ID: "sync-off", DisplayName: "Sync off", Enabled: true, SyncEnabled: false},
		{ID: "hidden", DisplayName: "Hidden", Enabled: true, SyncEnabled: true, Hidden: true},
	}
	var attempted []string
	var published []core.Event
	result, err := checkpointWatchedGames(context.Background(), games, func(_ context.Context, gameID string) (core.Snapshot, error) {
		attempted = append(attempted, gameID)
		switch gameID {
		case "unchanged":
			return core.Snapshot{}, snapshot.ErrUnchanged
		case "empty":
			return core.Snapshot{}, snapshot.ErrNoFiles
		case "failed":
			return core.Snapshot{}, errors.New("read failed")
		default:
			return core.Snapshot{ID: "new-snapshot", GameID: gameID}, nil
		}
	}, func(event core.Event) {
		published = append(published, event)
	})
	if err == nil || !strings.Contains(err.Error(), `checkpoint "Failed": read failed`) {
		t.Fatalf("checkpoint failure was not returned: %v", err)
	}
	if result.CheckedGames != 4 || result.CreatedSnapshots != 1 || result.UnchangedGames != 1 || result.NoFilesGames != 1 || result.BackupFailed != 1 {
		t.Fatalf("unexpected checkpoint result: %#v", result)
	}
	if len(attempted) != 4 || attempted[3] != "failed" {
		t.Fatalf("ineligible games were checked: %#v", attempted)
	}
	if len(published) != 1 || published[0].Type != "snapshot.failed" || published[0].GameID != "failed" {
		t.Fatalf("checkpoint failure event was not published: %#v", published)
	}
}
