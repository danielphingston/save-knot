package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/saveknot/saveknot/internal/catalog"
	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/discovery"
	"github.com/saveknot/saveknot/internal/events"
	"github.com/saveknot/saveknot/internal/remote"
	"github.com/saveknot/saveknot/internal/settings"
	"github.com/saveknot/saveknot/internal/snapshot"
	"github.com/saveknot/saveknot/internal/watcher"
)

type repository interface {
	UpsertGame(context.Context, core.Game) error
	ReplaceCatalogPaths(context.Context, string, []core.GamePath) error
	ListGames(context.Context) ([]core.Game, error)
	Game(context.Context, string) (core.Game, error)
	GamePaths(context.Context, string) ([]core.GamePath, error)
}

type pendingRepository interface {
	PendingSnapshots(context.Context) ([]core.Snapshot, error)
	PendingSnapshotsForGame(context.Context, string) ([]core.Snapshot, error)
	Snapshot(context.Context, string) (core.Snapshot, error)
	DeleteSnapshot(context.Context, string) error
	SnapshotsBeyond(context.Context, string, int) ([]core.Snapshot, error)
}

type registryRepository interface {
	ReplaceCatalogRegistry(context.Context, string, []core.RegistryPath) error
	GamePolicy(context.Context, string) (core.BackupPolicy, error)
}

type Coordinator struct {
	repository    repository
	pending       pendingRepository
	registry      registryRepository
	snapshots     *snapshot.Service
	syncer        *remote.Syncer
	reconciler    *remote.Reconciler
	settings      *settings.Manager
	paths         config.Paths
	events        *events.Bus
	watcher       *watcher.Manager
	backupMu      sync.Mutex
	reconcileMu   sync.Mutex
	diagnosticsMu sync.RWMutex
	diagnostics   core.Diagnostics
}

func (c *Coordinator) Diagnostics() core.Diagnostics {
	c.diagnosticsMu.RLock()
	defer c.diagnosticsMu.RUnlock()
	result := c.diagnostics
	result.Discovery.SteamRoots = append([]string(nil), result.Discovery.SteamRoots...)
	result.Discovery.Unmatched = append([]string(nil), result.Discovery.Unmatched...)
	return result
}

func NewCoordinator(
	repository repository,
	pending pendingRepository,
	registry registryRepository,
	snapshots *snapshot.Service,
	syncer *remote.Syncer,
	reconciler *remote.Reconciler,
	settings *settings.Manager,
	paths config.Paths,
	eventBus *events.Bus,
	watchManager *watcher.Manager,
) *Coordinator {
	return &Coordinator{
		repository: repository, pending: pending, registry: registry, snapshots: snapshots, syncer: syncer, reconciler: reconciler, settings: settings,
		paths: paths, events: eventBus, watcher: watchManager,
	}
}

func (c *Coordinator) Start(ctx context.Context) {
	c.watcher.Start(ctx, func(jobContext context.Context, gameID string) {
		created, err := c.Backup(jobContext, gameID)
		if err != nil && !errors.Is(err, snapshot.ErrNoFiles) && !errors.Is(err, snapshot.ErrUnchanged) {
			c.events.Publish(core.Event{Type: "snapshot.failed", GameID: gameID, Message: err.Error()})
			slog.Error("automatic snapshot failed", "game_id", gameID, "error", err)
			return
		}
		if err == nil {
			if syncErr := c.automaticSync(jobContext, created); syncErr != nil {
				c.events.Publish(core.Event{Type: "upload.failed", GameID: gameID, Message: syncErr.Error()})
				slog.Error("automatic R2 sync failed", "game_id", gameID, "snapshot_id", created.ID, "error", syncErr)
			}
		}
	})
	go c.run(ctx)
}

func (c *Coordinator) Close() error {
	return c.watcher.Close()
}

func (c *Coordinator) run(ctx context.Context) {
	c.refresh(ctx)
	if _, err := c.ReconcileRemote(ctx); err != nil && !errors.Is(err, settings.ErrR2NotConfigured) {
		slog.Error("startup R2 reconciliation failed", "error", err)
		c.events.Publish(core.Event{Type: "storage.error", Message: err.Error()})
	}
	catalogTicker := time.NewTicker(24 * time.Hour)
	discoveryTicker := time.NewTicker(5 * time.Minute)
	syncTicker := time.NewTicker(5 * time.Minute)
	defer catalogTicker.Stop()
	defer discoveryTicker.Stop()
	defer syncTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-catalogTicker.C:
			c.refresh(ctx)
		case <-discoveryTicker.C:
			c.discover(ctx, false)
		case <-syncTicker.C:
			if err := c.SyncPending(ctx); err != nil && !errors.Is(err, settings.ErrR2NotConfigured) {
				slog.Warn("retry pending R2 snapshots", "error", err)
			}
		}
	}
}

func (c *Coordinator) refresh(ctx context.Context) {
	checked := time.Now().UTC()
	cfg := c.settings.Config()
	fetcher := catalog.Fetcher{URL: cfg.ManifestURL, Path: c.paths.Catalog, ETagPath: c.paths.CatalogTag}
	updated, err := fetcher.Update(ctx)
	if err != nil {
		slog.Error("refresh Ludusavi catalog", "url", cfg.ManifestURL, "cache", c.paths.Catalog, "error", err)
		c.setCatalogDiagnostics(checked, false, 0, err)
		if _, statErr := os.Stat(c.paths.Catalog); statErr != nil {
			c.events.Publish(core.Event{Type: "catalog.failed", Message: err.Error()})
			return
		}
	} else if updated {
		slog.Info("Ludusavi catalog updated", "cache", c.paths.Catalog)
		c.events.Publish(core.Event{Type: "catalog.updated"})
	}
	c.discover(ctx, true)
}

func (c *Coordinator) discover(ctx context.Context, deep bool) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	manifest, err := catalog.Load(c.paths.Catalog)
	if err != nil {
		slog.Error("load Ludusavi catalog", "path", c.paths.Catalog, "error", err)
		c.setCatalogDiagnostics(time.Now().UTC(), false, 0, err)
		return
	}
	now := time.Now().UTC()
	c.setCatalogDiagnostics(now, true, len(manifest.Games), nil)
	roots := discovery.SteamRoots(c.settings.Config().SteamRoots)
	installed, err := discovery.DiscoverSteam(ctx, roots)
	if err != nil {
		slog.Error("discover Steam games", "roots", roots, "error", err)
		c.setDiscoveryDiagnostics(core.DiscoveryDiagnostics{LastRun: &now, SteamRoots: roots, LastError: err.Error()})
		c.events.Publish(core.Event{Type: "discovery.failed", Message: err.Error()})
		return
	}
	matched, registered, matchedNames, unmatched := c.registerSteamGames(ctx, manifest, installed, now)
	cfg := c.settings.Config()
	epicInstalled, epicErr := discovery.DiscoverEpic(ctx, cfg.EpicManifests)
	if epicErr != nil {
		slog.Error("discover Epic games", "error", epicErr)
	}
	gogInstalled, gogErr := discovery.DiscoverGOG(ctx, cfg.GOGRoots)
	if gogErr != nil {
		slog.Error("discover GOG games", "error", gogErr)
	}
	storeMatched, storeRegistered := c.registerInstalledGames(ctx, manifest, append(epicInstalled, gogInstalled...), now, matchedNames, &unmatched)
	matched += storeMatched
	registered += storeRegistered
	localFound, localRegistered, deepMillis, deepErr := c.registerLocalSaves(ctx, manifest, matchedNames, now, deep)
	registered += localRegistered
	if deepErr != nil {
		slog.Error("deep local-save discovery failed", "error", deepErr)
		c.setDiscoveryDiagnostics(core.DiscoveryDiagnostics{LastRun: &now, SteamRoots: roots, SteamInstalled: len(installed), EpicInstalled: len(epicInstalled), GOGInstalled: len(gogInstalled), CatalogMatched: matched, GamesRegistered: registered, DeepScanMillis: deepMillis, Unmatched: unmatched, LastError: deepErr.Error()})
		return
	}
	c.setDiscoveryDiagnostics(core.DiscoveryDiagnostics{LastRun: &now, SteamRoots: roots, SteamInstalled: len(installed), EpicInstalled: len(epicInstalled), GOGInstalled: len(gogInstalled), CatalogMatched: matched, GamesRegistered: registered, LocalSaveGames: localFound, DeepScanMillis: deepMillis, Unmatched: unmatched})
	slog.Info("game discovery completed", "catalog_games", len(manifest.Games), "steam_roots", len(roots), "steam_installed", len(installed), "epic_installed", len(epicInstalled), "gog_installed", len(gogInstalled), "catalog_matched", matched, "local_save_games", localFound, "registered", registered, "unmatched", len(unmatched), "deep_scan_ms", deepMillis)
	c.ReconcileWatches(ctx)
}

func (c *Coordinator) registerSteamGames(ctx context.Context, manifest *catalog.Manifest, installed []discovery.SteamGame, now time.Time) (int, int, map[string]struct{}, []string) {
	matched := 0
	registered := 0
	matchedNames := make(map[string]struct{})
	var unmatched []string
	for _, installation := range installed {
		definition, ok := manifest.SteamGame(installation.AppID)
		if !ok {
			if len(unmatched) < 25 {
				unmatched = append(unmatched, installation.Name+" ("+installation.AppID+")")
			}
			continue
		}
		matched++
		matchedNames[definition.Name] = struct{}{}
		if c.registerSteamGame(ctx, installation, definition, now) {
			registered++
		}
	}
	return matched, registered, matchedNames, unmatched
}

func (c *Coordinator) registerSteamGame(ctx context.Context, installation discovery.SteamGame, definition catalog.Definition, now time.Time) bool {
	game, paths, err := discovery.CatalogGame(installation, definition, now)
	if err != nil {
		slog.Warn("resolve Ludusavi save paths", "game", definition.Name, "app_id", installation.AppID, "error", err)
		return false
	}
	if err := c.repository.UpsertGame(ctx, game); err != nil {
		slog.Error("register discovered game", "game", definition.Name, "app_id", installation.AppID, "error", err)
		return false
	}
	if err := c.repository.ReplaceCatalogPaths(ctx, game.ID, paths); err != nil {
		slog.Error("store discovered save paths", "game", definition.Name, "app_id", installation.AppID, "error", err)
		return false
	}
	registryPaths, err := discovery.RegistryPaths(game.ID, "steam", installation.AppID, game.InstallPath, definition)
	if err != nil {
		slog.Warn("resolve Ludusavi registry paths", "game", definition.Name, "error", err)
		return false
	}
	if err := c.registry.ReplaceCatalogRegistry(ctx, game.ID, registryPaths); err != nil {
		slog.Error("store discovered registry paths", "game", definition.Name, "error", err)
		return false
	}
	c.events.Publish(core.Event{Type: "game.discovered", GameID: game.ID})
	return true
}

func (c *Coordinator) registerLocalSaves(ctx context.Context, manifest *catalog.Manifest, matchedNames map[string]struct{}, now time.Time, deep bool) (int, int, int64, error) {
	previous := c.Diagnostics().Discovery
	if !deep {
		return previous.LocalSaveGames, 0, previous.DeepScanMillis, nil
	}
	started := time.Now()
	localGames, err := discovery.DiscoverLocalSaves(ctx, manifest, matchedNames, now)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		return 0, 0, duration, err
	}
	registered := 0
	for _, found := range localGames {
		if err := c.repository.UpsertGame(ctx, found.Game); err != nil {
			slog.Error("register local save game", "game", found.Game.DisplayName, "error", err)
			continue
		}
		if err := c.repository.ReplaceCatalogPaths(ctx, found.Game.ID, found.Paths); err != nil {
			slog.Error("store local save paths", "game", found.Game.DisplayName, "error", err)
			continue
		}
		registered++
		c.events.Publish(core.Event{Type: "game.discovered", GameID: found.Game.ID})
	}
	return len(localGames), registered, duration, nil
}

func (c *Coordinator) registerInstalledGames(ctx context.Context, manifest *catalog.Manifest, installed []discovery.InstalledGame, now time.Time, matchedNames map[string]struct{}, unmatched *[]string) (int, int) {
	matched := 0
	registered := 0
	for _, installation := range installed {
		definition, ok := manifest.MatchName(installation.Name, installation.GameDir)
		if !ok {
			if len(*unmatched) < 25 {
				*unmatched = append(*unmatched, installation.Store+": "+installation.Name)
			}
			continue
		}
		matched++
		matchedNames[definition.Name] = struct{}{}
		game, paths, err := discovery.CatalogInstalledGame(installation, definition, now)
		if err != nil {
			slog.Warn("resolve store save paths", "store", installation.Store, "game", definition.Name, "error", err)
			continue
		}
		if err := c.repository.UpsertGame(ctx, game); err != nil {
			slog.Error("register store game", "store", installation.Store, "game", definition.Name, "error", err)
			continue
		}
		if err := c.repository.ReplaceCatalogPaths(ctx, game.ID, paths); err != nil {
			slog.Error("store discovered game paths", "store", installation.Store, "game", definition.Name, "error", err)
			continue
		}
		registryPaths, err := discovery.RegistryPaths(game.ID, installation.Store, game.StoreID, game.InstallPath, definition)
		if err != nil {
			slog.Warn("resolve store registry paths", "store", installation.Store, "game", definition.Name, "error", err)
			continue
		}
		if err := c.registry.ReplaceCatalogRegistry(ctx, game.ID, registryPaths); err != nil {
			slog.Error("store discovered registry paths", "store", installation.Store, "game", definition.Name, "error", err)
			continue
		}
		registered++
		c.events.Publish(core.Event{Type: "game.discovered", GameID: game.ID})
	}
	return matched, registered
}

func (c *Coordinator) DiscoverNow(ctx context.Context) core.Diagnostics {
	c.refresh(ctx)
	return c.Diagnostics()
}

func (c *Coordinator) SearchCatalog(query string) ([]core.CatalogChoice, error) {
	manifest, err := catalog.Load(c.paths.Catalog)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	choices := make([]core.CatalogChoice, 0, 20)
	for name, definition := range manifest.Games {
		if definition.Alias != "" || query != "" && !strings.Contains(strings.ToLower(name), query) {
			continue
		}
		choices = append(choices, core.CatalogChoice{ID: name, Name: name})
	}
	sort.Slice(choices, func(i, j int) bool { return choices[i].Name < choices[j].Name })
	if len(choices) > 20 {
		choices = choices[:20]
	}
	return choices, nil
}

func (c *Coordinator) RemapGame(ctx context.Context, gameID, catalogID string) error {
	manifest, err := catalog.Load(c.paths.Catalog)
	if err != nil {
		return err
	}
	definition, ok := manifest.Resolve(catalogID)
	if !ok {
		return fmt.Errorf("catalog game %q was not found", catalogID)
	}
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return err
	}
	if game.InstallPath == "" {
		return errors.New("a manual game without an install location cannot be remapped; add save locations directly")
	}
	root := filepath.Dir(game.InstallPath)
	if game.Store == "steam" {
		root = filepath.Dir(filepath.Dir(filepath.Dir(game.InstallPath)))
	}
	installed := discovery.InstalledGame{
		Store: game.Store, StoreID: game.StoreID, Name: game.DisplayName, InstallPath: game.InstallPath,
		Root: root, GameDir: filepath.Base(game.InstallPath),
	}
	remapped, paths, err := discovery.CatalogInstalledGame(installed, definition, time.Now().UTC())
	if err != nil {
		return err
	}
	remapped.ID = game.ID
	remapped.DisplayName = game.DisplayName
	if err := c.repository.UpsertGame(ctx, remapped); err != nil {
		return err
	}
	if err := c.repository.ReplaceCatalogPaths(ctx, game.ID, paths); err != nil {
		return err
	}
	registryPaths, err := discovery.RegistryPaths(game.ID, game.Store, game.StoreID, game.InstallPath, definition)
	if err != nil {
		return err
	}
	if err := c.registry.ReplaceCatalogRegistry(ctx, game.ID, registryPaths); err != nil {
		return err
	}
	c.ReconcileWatches(ctx)
	return nil
}

func (c *Coordinator) ConfigureLocal(ctx context.Context, local config.Local) error {
	if !filepath.IsAbs(filepath.Clean(local.LocalBackupDir)) {
		return errors.New("local backup location must be an absolute path")
	}
	if err := os.MkdirAll(local.LocalBackupDir, 0o700); err != nil {
		return fmt.Errorf("create local backup location: %w", err)
	}
	if err := c.settings.ConfigureLocal(local); err != nil {
		return err
	}
	c.snapshots.SetBlobRoot(filepath.Clean(local.LocalBackupDir))
	c.discover(ctx, true)
	return nil
}

func (c *Coordinator) ReconcileWatches(ctx context.Context) {
	games, err := c.repository.ListGames(ctx)
	if err != nil {
		slog.Error("list games for watcher reconciliation", "error", err)
		return
	}
	var targets []watcher.Target
	for _, game := range games {
		if !game.Enabled {
			continue
		}
		paths, err := c.repository.GamePaths(ctx, game.ID)
		if err != nil {
			slog.Error("list save paths for watcher", "game_id", game.ID, "error", err)
			continue
		}
		policy, err := c.registry.GamePolicy(ctx, game.ID)
		if err != nil {
			slog.Error("load game backup policy for watcher", "game_id", game.ID, "error", err)
			continue
		}
		for _, path := range paths {
			if path.Enabled {
				targets = append(targets, watcher.Target{GameID: game.ID, Path: path.Resolved, Policy: policy})
			}
		}
	}
	c.watcher.Reconcile(targets)
	slog.Debug("filesystem watches reconciled", "targets", len(targets))
}

func (c *Coordinator) setCatalogDiagnostics(checked time.Time, loaded bool, count int, diagnosticErr error) {
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	c.diagnostics.Catalog.Loaded = loaded
	c.diagnostics.Catalog.GameCount = count
	c.diagnostics.Catalog.CachePath = c.paths.Catalog
	c.diagnostics.Catalog.LastChecked = &checked
	c.diagnostics.Catalog.LastError = errorMessage(diagnosticErr)
}

func (c *Coordinator) setDiscoveryDiagnostics(diagnostics core.DiscoveryDiagnostics) {
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	diagnostics.SteamRoots = append([]string(nil), diagnostics.SteamRoots...)
	diagnostics.Unmatched = append([]string(nil), diagnostics.Unmatched...)
	c.diagnostics.Discovery = diagnostics
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (c *Coordinator) Backup(ctx context.Context, gameID string) (core.Snapshot, error) {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return core.Snapshot{}, err
	}
	c.events.Publish(core.Event{Type: "snapshot.started", GameID: gameID})
	created, err := c.snapshots.Create(ctx, game)
	if err != nil {
		return core.Snapshot{}, err
	}
	c.events.Publish(core.Event{Type: "snapshot.completed", GameID: gameID, Data: map[string]any{"snapshotId": created.ID, "remote": false}})
	if err := c.applyLocalRetention(ctx, gameID); err != nil {
		slog.Error("apply snapshot retention", "game_id", gameID, "error", err)
	}
	return created, nil
}

func (c *Coordinator) applyLocalRetention(ctx context.Context, gameID string) error {
	keep := c.settings.Config().RetentionKeep
	if keep == 0 {
		keep = 50
	}
	excess, err := c.pending.SnapshotsBeyond(ctx, gameID, keep)
	if err != nil {
		return err
	}
	for _, target := range excess {
		if target.RemoteState == "synced" {
			continue
		}
		if err := c.pending.DeleteSnapshot(ctx, target.ID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) Restore(ctx context.Context, gameID, snapshotID string) (core.Snapshot, error) {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return core.Snapshot{}, err
	}
	target, err := c.pending.Snapshot(ctx, snapshotID)
	if err != nil {
		return core.Snapshot{}, err
	}
	needsDownload, err := c.syncer.NeedsDownload(ctx, target)
	if err != nil {
		return core.Snapshot{}, err
	}
	if needsDownload {
		r2, err := c.settings.R2(ctx)
		if err != nil {
			return core.Snapshot{}, fmt.Errorf("download remote snapshot data: %w", err)
		}
		if err := c.syncer.EnsureLocal(ctx, r2, target, c.snapshots.BlobRoot()); err != nil {
			return core.Snapshot{}, err
		}
	}
	c.events.Publish(core.Event{Type: "restore.started", GameID: gameID})
	preRestore, err := c.snapshots.Restore(ctx, game, snapshotID)
	if err != nil {
		return core.Snapshot{}, err
	}
	c.events.Publish(core.Event{Type: "restore.completed", GameID: gameID, Data: map[string]any{"snapshotId": snapshotID}})
	return preRestore, nil
}

func (c *Coordinator) ReconcileRemote(ctx context.Context) (remote.ReconcileResult, error) {
	r2, err := c.settings.R2(ctx)
	if err != nil {
		return remote.ReconcileResult{}, err
	}
	c.events.Publish(core.Event{Type: "storage.reconcile.started"})
	result, err := c.reconciler.Reconcile(ctx, r2)
	if err != nil {
		return result, err
	}
	c.events.Publish(core.Event{Type: "storage.reconcile.completed", Data: map[string]any{"objects": result.Objects, "snapshots": result.Snapshots, "skipped": result.Skipped, "games": result.Games}})
	slog.Info("R2 snapshot reconciliation completed", "objects", result.Objects, "snapshots", result.Snapshots, "skipped", result.Skipped, "games", result.Games)
	return result, nil
}

func (c *Coordinator) SyncPending(ctx context.Context) error {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	r2, err := c.settings.R2(ctx)
	if err != nil {
		return err
	}
	pending, err := c.pending.PendingSnapshots(ctx)
	if err != nil {
		return err
	}
	if err := c.uploadAll(ctx, r2, pending); err != nil {
		return err
	}
	games := make(map[string]struct{})
	for _, pendingSnapshot := range pending {
		games[pendingSnapshot.GameID] = struct{}{}
	}
	for gameID := range games {
		if err := c.applyRetention(ctx, gameID); err != nil {
			return err
		}
	}
	return nil
}

func (c *Coordinator) SyncGame(ctx context.Context, gameID string) error {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return err
	}
	if !game.SyncEnabled {
		return errors.New("R2 sync is disabled for this game")
	}
	r2, err := c.settings.R2(ctx)
	if err != nil {
		return err
	}
	pending, err := c.pending.PendingSnapshotsForGame(ctx, gameID)
	if err != nil {
		return err
	}
	if err := c.uploadAll(ctx, r2, pending); err != nil {
		return err
	}
	return c.applyRetention(ctx, gameID)
}

func (c *Coordinator) automaticSync(ctx context.Context, target core.Snapshot) error {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	game, err := c.repository.Game(ctx, target.GameID)
	if err != nil {
		return err
	}
	if !game.SyncEnabled {
		return nil
	}
	if err := c.syncOne(ctx, target); errors.Is(err, settings.ErrR2NotConfigured) {
		return nil
	} else if err != nil {
		return err
	}
	return c.applyRetention(ctx, target.GameID)
}

func (c *Coordinator) applyRetention(ctx context.Context, gameID string) error {
	keep := c.settings.Config().RetentionKeep
	if keep == 0 {
		keep = 50
	}
	excess, err := c.pending.SnapshotsBeyond(ctx, gameID, keep)
	if err != nil {
		return err
	}
	r2, err := c.retentionRemote(ctx, excess)
	if errors.Is(err, settings.ErrR2NotConfigured) {
		r2 = nil
	} else if err != nil {
		return err
	}
	for _, target := range excess {
		if target.RemoteState == "synced" && r2 == nil {
			continue
		}
		if target.RemoteState == "synced" {
			key := path.Join("games", target.GameID, "snapshots", target.ID+".json")
			if err := r2.Delete(ctx, key); err != nil {
				return err
			}
		}
		if err := c.pending.DeleteSnapshot(ctx, target.ID); err != nil {
			return err
		}
		c.events.Publish(core.Event{Type: "snapshot.retained", GameID: gameID, Data: map[string]any{"deletedSnapshotId": target.ID}})
	}
	return nil
}

func (c *Coordinator) retentionRemote(ctx context.Context, snapshots []core.Snapshot) (*remote.R2, error) {
	for _, target := range snapshots {
		if target.RemoteState != "synced" {
			continue
		}
		r2, err := c.settings.R2(ctx)
		if errors.Is(err, settings.ErrR2NotConfigured) {
			return nil, settings.ErrR2NotConfigured
		}
		return r2, err
	}
	return nil, settings.ErrR2NotConfigured
}

func (c *Coordinator) syncOne(ctx context.Context, target core.Snapshot) error {
	r2, err := c.settings.R2(ctx)
	if err != nil {
		return err
	}
	if err := c.syncer.Upload(ctx, r2, target); err != nil {
		return err
	}
	c.events.Publish(core.Event{Type: "upload.completed", GameID: target.GameID, Data: map[string]any{"snapshotId": target.ID}})
	return nil
}

func (c *Coordinator) uploadAll(ctx context.Context, r2 *remote.R2, snapshots []core.Snapshot) error {
	for _, pendingSnapshot := range snapshots {
		if err := c.syncer.Upload(ctx, r2, pendingSnapshot); err != nil {
			return err
		}
		c.events.Publish(core.Event{Type: "upload.completed", GameID: pendingSnapshot.GameID, Data: map[string]any{"snapshotId": pendingSnapshot.ID}})
	}
	return nil
}

func (c *Coordinator) DeleteSnapshot(ctx context.Context, gameID, snapshotID string, remoteToo bool) error {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	target, err := c.pending.Snapshot(ctx, snapshotID)
	if err != nil {
		return err
	}
	if target.GameID != gameID {
		return errors.New("snapshot does not belong to this game")
	}
	if remoteToo && target.RemoteState == "synced" {
		r2, err := c.settings.R2(ctx)
		if err != nil {
			return fmt.Errorf("load R2 connection before delete: %w", err)
		}
		key := path.Join("games", target.GameID, "snapshots", target.ID+".json")
		if err := r2.Delete(ctx, key); err != nil {
			return err
		}
	}
	if err := c.pending.DeleteSnapshot(ctx, snapshotID); err != nil {
		return err
	}
	c.events.Publish(core.Event{Type: "snapshot.deleted", GameID: gameID, Data: map[string]any{"snapshotId": snapshotID, "remote": remoteToo}})
	return nil
}
