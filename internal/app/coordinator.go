package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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

type atomicGameRelationsRepository interface {
	SaveGameRelations(context.Context, core.Game, []core.GamePath, []core.RegistryPath, bool) error
}

func (c *Coordinator) saveGameRelations(ctx context.Context, game core.Game, paths []core.GamePath, registry []core.RegistryPath, replaceRegistry bool) error {
	if repo, ok := c.repository.(atomicGameRelationsRepository); ok {
		return repo.SaveGameRelations(ctx, game, paths, registry, replaceRegistry)
	}
	if err := c.repository.UpsertGame(ctx, game); err != nil {
		return err
	}
	if err := c.repository.ReplaceCatalogPaths(ctx, game.ID, paths); err != nil {
		return err
	}
	if replaceRegistry {
		return c.registry.ReplaceCatalogRegistry(ctx, game.ID, registry)
	}
	return nil
}

type diagnosticsRepository interface {
	SaveDiagnostics(context.Context, core.Diagnostics) error
	LoadDiagnostics(context.Context) (core.Diagnostics, bool, error)
}

type gameStateRepository interface {
	SetGameHidden(context.Context, string, bool) error
}

type pendingRepository interface {
	PendingSnapshotsForGame(context.Context, string) ([]core.Snapshot, error)
	Snapshot(context.Context, string) (core.Snapshot, error)
	DeleteSnapshot(context.Context, string) error
	SnapshotsBeyond(context.Context, string, int) ([]core.Snapshot, error)
}

type periodicPendingRepository interface {
	PendingSnapshotsForWatchedGames(context.Context) ([]core.Snapshot, error)
}

type registryRepository interface {
	ReplaceCatalogRegistry(context.Context, string, []core.RegistryPath) error
	GamePolicy(context.Context, string) (core.BackupPolicy, error)
}

type Coordinator struct {
	repository      repository
	diagnosticCache diagnosticsRepository
	gameState       gameStateRepository
	pending         pendingRepository
	periodic        periodicPendingRepository
	registry        registryRepository
	snapshots       *snapshot.Service
	syncer          *remote.Syncer
	reconciler      *remote.Reconciler
	settings        *settings.Manager
	paths           config.Paths
	events          *events.Bus
	watcher         *watcher.Manager
	backupMu        sync.Mutex
	syncMu          sync.Mutex
	syncNowActive   atomic.Bool
	reconcileMu     sync.Mutex
	diagnosticsMu   sync.RWMutex
	diagnostics     core.Diagnostics
}

func (c *Coordinator) Diagnostics() core.Diagnostics {
	c.diagnosticsMu.RLock()
	defer c.diagnosticsMu.RUnlock()
	result := c.diagnostics
	result.Discovery.SteamRoots = append([]string(nil), result.Discovery.SteamRoots...)
	result.Discovery.Unmatched = append([]string(nil), result.Discovery.Unmatched...)
	return result
}

func (c *Coordinator) loadCachedDiagnostics(ctx context.Context) {
	diagnostics, found, err := c.diagnosticCache.LoadDiagnostics(ctx)
	if err != nil {
		slog.Warn("load cached discovery diagnostics", "error", err)
		return
	}
	if !found {
		return
	}
	diagnostics.Discovery.SteamRoots = append([]string(nil), diagnostics.Discovery.SteamRoots...)
	diagnostics.Discovery.Unmatched = append([]string(nil), diagnostics.Discovery.Unmatched...)
	c.diagnosticsMu.Lock()
	c.diagnostics = diagnostics
	c.diagnosticsMu.Unlock()
}

func NewCoordinator(
	repository repository,
	diagnosticCache diagnosticsRepository,
	gameState gameStateRepository,
	pending pendingRepository,
	periodic periodicPendingRepository,
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
		repository: repository, diagnosticCache: diagnosticCache, gameState: gameState, pending: pending, periodic: periodic, registry: registry, snapshots: snapshots, syncer: syncer, reconciler: reconciler, settings: settings,
		paths: paths, events: eventBus, watcher: watchManager,
	}
}

func (c *Coordinator) Start(ctx context.Context) {
	c.watcher.Start(ctx, c.automaticBackup)
	c.ReconcileWatches(ctx)
	go c.run(ctx)
}

func (c *Coordinator) automaticBackup(ctx context.Context, gameID string) {
	created, err := c.Backup(ctx, gameID)
	if err != nil {
		if !errors.Is(err, snapshot.ErrNoFiles) && !errors.Is(err, snapshot.ErrUnchanged) {
			slog.Error("automatic snapshot failed", "game_id", gameID, "error", err)
		}
		return
	}
	if syncErr := c.automaticSync(ctx, created); syncErr != nil {
		c.events.PublishContext(ctx, core.Event{Type: "upload.failed", GameID: gameID, Message: syncErr.Error()})
		slog.Error("automatic R2 sync failed", "game_id", gameID, "snapshot_id", created.ID, "error", syncErr)
	}
}

func (c *Coordinator) Close() error {
	return c.watcher.Close()
}

func (c *Coordinator) run(ctx context.Context) {
	c.refreshCatalog(ctx)
	if _, err := c.ReconcileRemote(ctx); err != nil && !errors.Is(err, settings.ErrR2NotConfigured) {
		slog.Error("startup R2 reconciliation failed", "error", err)
		c.events.PublishContext(ctx, core.Event{Type: "storage.error", Message: err.Error()})
	}
	catalogTicker := time.NewTicker(24 * time.Hour)
	automationTicker := time.NewTicker(time.Minute)
	lastSync := time.Now()
	lastDiscovery := time.Now()
	defer catalogTicker.Stop()
	defer automationTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-catalogTicker.C:
			c.refreshCatalog(ctx)
		case now := <-automationTicker.C:
			automation := c.settings.Config().Automation
			if automation.PeriodicSyncEnabled && now.Sub(lastSync) >= time.Duration(automation.SyncIntervalMinutes)*time.Minute {
				lastSync = now
				if _, err := c.SyncNow(ctx); err != nil && !errors.Is(err, settings.ErrR2NotConfigured) && !errors.Is(err, core.ErrSyncInProgress) {
					slog.Warn("periodic sync of watched games", "error", err)
				}
			}
			if automation.PeriodicDiscoveryEnabled && now.Sub(lastDiscovery) >= time.Duration(automation.DiscoveryIntervalMinutes)*time.Minute {
				lastDiscovery = now
				c.discover(ctx)
			}
		}
	}
}

func (c *Coordinator) refreshCatalog(ctx context.Context) {
	checked := time.Now().UTC()
	cfg := c.settings.Config()
	fetcher := catalog.Fetcher{URL: cfg.ManifestURL, Path: c.paths.Catalog, IndexPath: c.paths.CatalogDB, ETagPath: c.paths.CatalogTag}
	updated, err := fetcher.Update(ctx)
	if err != nil {
		slog.Error("refresh Ludusavi catalog", "url", cfg.ManifestURL, "cache", c.paths.Catalog, "error", err)
		c.setCatalogDiagnostics(ctx, checked, false, 0, err)
		if indexErr := catalog.EnsureIndex(ctx, c.paths.Catalog, c.paths.CatalogDB); indexErr != nil {
			c.events.PublishContext(ctx, core.Event{Type: "catalog.failed", Message: err.Error()})
			return
		}
	} else if updated {
		slog.Info("Ludusavi catalog updated", "cache", c.paths.Catalog)
		c.events.PublishContext(ctx, core.Event{Type: "catalog.updated"})
	}
	count, err := (catalog.Index{Path: c.paths.CatalogDB}).Count(ctx)
	if err != nil {
		slog.Error("inspect Ludusavi catalog index", "path", c.paths.CatalogDB, "error", err)
		c.setCatalogDiagnostics(ctx, checked, false, 0, err)
		return
	}
	c.setCatalogDiagnostics(ctx, checked, true, count, nil)
}

func (c *Coordinator) discover(ctx context.Context) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	index := catalog.Index{Path: c.paths.CatalogDB}
	count, err := index.Count(ctx)
	if err != nil {
		slog.Error("load Ludusavi catalog index", "path", c.paths.CatalogDB, "error", err)
		c.setCatalogDiagnostics(ctx, time.Now().UTC(), false, 0, err)
		return
	}
	now := time.Now().UTC()
	c.setCatalogDiagnostics(ctx, now, true, count, nil)
	roots := discovery.SteamRoots(c.settings.Config().SteamRoots)
	installed, err := discovery.DiscoverSteam(ctx, roots)
	if err != nil {
		slog.Error("discover Steam games", "roots", roots, "error", err)
		c.setDiscoveryDiagnostics(ctx, core.DiscoveryDiagnostics{LastRun: &now, SteamRoots: roots, LastError: err.Error()})
		c.events.PublishContext(ctx, core.Event{Type: "discovery.failed", Message: err.Error()})
		return
	}
	matched, registered, matchedNames, unmatched := c.registerSteamGames(ctx, index, installed, now)
	cfg := c.settings.Config()
	epicInstalled, epicErr := discovery.DiscoverEpic(ctx, cfg.EpicManifests)
	if epicErr != nil {
		slog.Error("discover Epic games", "error", epicErr)
	}
	gogInstalled, gogErr := discovery.DiscoverGOG(ctx, cfg.GOGRoots)
	if gogErr != nil {
		slog.Error("discover GOG games", "error", gogErr)
	}
	storeMatched, storeRegistered := c.registerInstalledGames(ctx, index, append(epicInstalled, gogInstalled...), now, matchedNames, &unmatched)
	matched += storeMatched
	registered += storeRegistered
	localFound, localRegistered, deepMillis, deepErr := c.registerLocalSaves(ctx, index, matchedNames, now)
	registered += localRegistered
	if deepErr != nil {
		slog.Error("deep local-save discovery failed", "error", deepErr)
		c.setDiscoveryDiagnostics(ctx, core.DiscoveryDiagnostics{LastRun: &now, SteamRoots: roots, SteamInstalled: len(installed), EpicInstalled: len(epicInstalled), GOGInstalled: len(gogInstalled), CatalogMatched: matched, GamesRegistered: registered, DeepScanMillis: deepMillis, Unmatched: unmatched, LastError: deepErr.Error()})
		return
	}
	c.setDiscoveryDiagnostics(ctx, core.DiscoveryDiagnostics{LastRun: &now, SteamRoots: roots, SteamInstalled: len(installed), EpicInstalled: len(epicInstalled), GOGInstalled: len(gogInstalled), CatalogMatched: matched, GamesRegistered: registered, LocalSaveGames: localFound, DeepScanMillis: deepMillis, Unmatched: unmatched})
	slog.Info("game discovery completed", "catalog_games", count, "steam_roots", len(roots), "steam_installed", len(installed), "epic_installed", len(epicInstalled), "gog_installed", len(gogInstalled), "catalog_matched", matched, "local_save_games", localFound, "registered", registered, "unmatched", len(unmatched), "deep_scan_ms", deepMillis)
	c.ReconcileWatches(ctx)
}

func (c *Coordinator) registerSteamGames(ctx context.Context, index catalog.Index, installed []discovery.SteamGame, now time.Time) (int, int, map[string]struct{}, []string) {
	matched := 0
	registered := 0
	matchedNames := make(map[string]struct{})
	var unmatched []string
	seenApps := make(map[string]struct{}, len(installed))
	for _, installation := range installed {
		if _, seen := seenApps[installation.AppID]; seen {
			continue
		}
		seenApps[installation.AppID] = struct{}{}
		definition, ok, err := index.SteamGame(ctx, installation.AppID)
		if err != nil {
			slog.Error("look up Steam game in catalog index", "app_id", installation.AppID, "error", err)
			continue
		}
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
	registryPaths, err := discovery.RegistryPaths(game.ID, "steam", installation.AppID, game.InstallPath, definition)
	if err != nil {
		slog.Warn("resolve Ludusavi registry paths", "game", definition.Name, "error", err)
		return false
	}
	isNew := c.gameIsNew(ctx, game.ID)
	if err := c.saveGameRelations(ctx, game, paths, registryPaths, true); err != nil {
		slog.Error("register discovered game and paths", "game", definition.Name, "app_id", installation.AppID, "error", err)
		return false
	}
	if isNew {
		c.events.PublishContext(ctx, core.Event{Type: "game.discovered", GameID: game.ID})
	}
	return true
}

func (c *Coordinator) registerLocalSaves(ctx context.Context, index catalog.Index, matchedNames map[string]struct{}, now time.Time) (int, int, int64, error) {
	started := time.Now()
	localGames, err := discovery.DiscoverLocalSaves(ctx, index, matchedNames, now)
	duration := time.Since(started).Milliseconds()
	if err != nil {
		return 0, 0, duration, err
	}
	registered := 0
	for _, found := range localGames {
		isNew := c.gameIsNew(ctx, found.Game.ID)
		if err := c.saveGameRelations(ctx, found.Game, found.Paths, nil, false); err != nil {
			slog.Error("register local save game and paths", "game", found.Game.DisplayName, "error", err)
			continue
		}
		registered++
		if isNew {
			c.events.PublishContext(ctx, core.Event{Type: "game.discovered", GameID: found.Game.ID})
		}
	}
	return len(localGames), registered, duration, nil
}

func (c *Coordinator) registerInstalledGames(ctx context.Context, index catalog.Index, installed []discovery.InstalledGame, now time.Time, matchedNames map[string]struct{}, unmatched *[]string) (int, int) {
	matched := 0
	registered := 0
	for _, installation := range installed {
		definition, ok, err := index.MatchName(ctx, installation.Name, installation.GameDir)
		if err != nil {
			slog.Error("look up installed game in catalog index", "store", installation.Store, "name", installation.Name, "error", err)
			continue
		}
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
		registryPaths, err := discovery.RegistryPaths(game.ID, installation.Store, game.StoreID, game.InstallPath, definition)
		if err != nil {
			slog.Warn("resolve store registry paths", "store", installation.Store, "game", definition.Name, "error", err)
			continue
		}
		isNew := c.gameIsNew(ctx, game.ID)
		if err := c.saveGameRelations(ctx, game, paths, registryPaths, true); err != nil {
			slog.Error("register store game and paths", "store", installation.Store, "game", definition.Name, "error", err)
			continue
		}
		registered++
		if isNew {
			c.events.PublishContext(ctx, core.Event{Type: "game.discovered", GameID: game.ID})
		}
	}
	return matched, registered
}

func (c *Coordinator) gameIsNew(ctx context.Context, gameID string) bool {
	_, err := c.repository.Game(ctx, gameID)
	if errors.Is(err, core.ErrGameNotFound) {
		return true
	}
	if err != nil {
		slog.Warn("inspect discovered game before upsert", "game_id", gameID, "error", err)
	}
	return false
}

func (c *Coordinator) DiscoverNow(ctx context.Context) core.Diagnostics {
	c.refreshCatalog(ctx)
	c.discover(ctx)
	return c.Diagnostics()
}

func (c *Coordinator) SearchCatalog(ctx context.Context, query string) ([]core.CatalogChoice, error) {
	names, err := (catalog.Index{Path: c.paths.CatalogDB}).Search(ctx, strings.TrimSpace(query), 20)
	if err != nil {
		return nil, err
	}
	choices := make([]core.CatalogChoice, 0, len(names))
	for _, name := range names {
		choices = append(choices, core.CatalogChoice{ID: name, Name: name})
	}
	return choices, nil
}

func (c *Coordinator) RemapGame(ctx context.Context, gameID, catalogID string) error {
	definition, ok, err := (catalog.Index{Path: c.paths.CatalogDB}).Resolve(ctx, catalogID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("catalog game %q was not found", catalogID)
	}
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return err
	}
	if game.InstallPath == "" {
		if game.Store != "custom" {
			return errors.New("a discovered game without an install location cannot be remapped")
		}
		// Manual games already have explicit save locations. Link the catalog
		// metadata without replacing those user-selected paths with paths that
		// would require a detected installation to resolve safely.
		game.CatalogID = definition.Name
		game.CatalogName = definition.Name
		return c.saveGameRelations(ctx, game, nil, nil, false)
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
	registryPaths, err := discovery.RegistryPaths(game.ID, game.Store, game.StoreID, game.InstallPath, definition)
	if err != nil {
		return err
	}
	if err := c.saveGameRelations(ctx, remapped, paths, registryPaths, true); err != nil {
		return err
	}
	c.ReconcileWatches(ctx)
	return nil
}

func (c *Coordinator) ConfigureLocal(ctx context.Context, local config.Local) error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	if !filepath.IsAbs(filepath.Clean(local.LocalBackupDir)) {
		return errors.New("local backup location must be an absolute path")
	}
	if err := os.MkdirAll(local.LocalBackupDir, 0o700); err != nil {
		return fmt.Errorf("create local backup location: %w", err)
	}
	oldRoot := c.snapshots.BlobRoot()
	if relocator, ok := c.repository.(interface {
		RelocateBlobs(context.Context, string) error
	}); ok {
		if err := relocator.RelocateBlobs(ctx, filepath.Clean(local.LocalBackupDir)); err != nil {
			return fmt.Errorf("move local backup blobs: %w", err)
		}
	}
	if err := c.settings.ConfigureLocal(local); err != nil {
		if relocator, ok := c.repository.(interface {
			RelocateBlobs(context.Context, string) error
		}); ok {
			_ = relocator.RelocateBlobs(ctx, oldRoot)
		}
		return err
	}
	c.snapshots.SetBlobRoot(filepath.Clean(local.LocalBackupDir))
	c.ReconcileWatches(ctx)
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

func (c *Coordinator) setCatalogDiagnostics(ctx context.Context, checked time.Time, loaded bool, count int, diagnosticErr error) {
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	c.diagnostics.Catalog.Loaded = loaded
	c.diagnostics.Catalog.GameCount = count
	c.diagnostics.Catalog.CachePath = c.paths.Catalog
	c.diagnostics.Catalog.LastChecked = &checked
	c.diagnostics.Catalog.LastError = errorMessage(diagnosticErr)
	c.persistDiagnostics(ctx)
}

func (c *Coordinator) setDiscoveryDiagnostics(ctx context.Context, diagnostics core.DiscoveryDiagnostics) {
	c.diagnosticsMu.Lock()
	defer c.diagnosticsMu.Unlock()
	diagnostics.SteamRoots = append([]string(nil), diagnostics.SteamRoots...)
	diagnostics.Unmatched = append([]string(nil), diagnostics.Unmatched...)
	c.diagnostics.Discovery = diagnostics
	c.persistDiagnostics(ctx)
}

// persistDiagnostics is called with diagnosticsMu held so concurrent catalog
// and discovery updates cannot overwrite each other with an older snapshot.
func (c *Coordinator) persistDiagnostics(ctx context.Context) {
	updatedAt := time.Now().UTC()
	c.diagnostics.UpdatedAt = &updatedAt
	if err := c.diagnosticCache.SaveDiagnostics(ctx, c.diagnostics); err != nil {
		slog.Warn("cache discovery diagnostics", "error", err)
	}
}

func errorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (c *Coordinator) Backup(ctx context.Context, gameID string) (core.Snapshot, error) {
	c.backupMu.Lock()
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		c.backupMu.Unlock()
		return core.Snapshot{}, err
	}
	if game.Hidden {
		c.backupMu.Unlock()
		return core.Snapshot{}, core.ErrGameNotFound
	}
	c.events.PublishContext(ctx, core.Event{Type: "snapshot.started", GameID: gameID})
	created, err := c.snapshots.Create(ctx, game)
	if err != nil {
		c.backupMu.Unlock()
		eventType := "snapshot.failed"
		if errors.Is(err, snapshot.ErrNoFiles) || errors.Is(err, snapshot.ErrUnchanged) {
			eventType = "snapshot.skipped"
		}
		c.events.PublishContext(ctx, core.Event{Type: eventType, GameID: gameID, Message: err.Error()})
		return core.Snapshot{}, err
	}
	c.backupMu.Unlock()
	c.events.PublishContext(ctx, core.Event{Type: "snapshot.completed", GameID: gameID, Data: map[string]any{"snapshotId": created.ID, "remote": false}})
	// Retention is serialized with remote sync and other mutations. Snapshot
	// creation has already committed locally, so network I/O never holds its lock.
	c.syncMu.Lock()
	c.backupMu.Lock()
	if err := c.applyLocalRetention(ctx, gameID); err != nil {
		slog.Error("apply snapshot retention", "game_id", gameID, "error", err)
	}
	c.backupMu.Unlock()
	c.syncMu.Unlock()
	return created, nil
}

func (c *Coordinator) SetGameHidden(ctx context.Context, gameID string, hidden bool) error {
	c.backupMu.Lock()
	if _, err := c.repository.Game(ctx, gameID); err != nil {
		c.backupMu.Unlock()
		return err
	}
	if err := c.gameState.SetGameHidden(ctx, gameID, hidden); err != nil {
		c.backupMu.Unlock()
		return err
	}
	c.backupMu.Unlock()
	c.ReconcileWatches(ctx)
	return nil
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
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
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
			c.settings.RecordR2Failure(err)
			return core.Snapshot{}, fmt.Errorf("download remote snapshot data: %w", err)
		}
		if err := c.syncer.EnsureLocal(ctx, r2, target, c.snapshots.BlobRoot()); err != nil {
			c.settings.RecordR2Failure(err)
			return core.Snapshot{}, err
		}
		c.settings.RecordR2Success()
	}
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	c.events.PublishContext(ctx, core.Event{Type: "restore.started", GameID: gameID})
	preRestore, err := c.snapshots.Restore(ctx, game, snapshotID)
	if err != nil {
		return core.Snapshot{}, err
	}
	c.events.PublishContext(ctx, core.Event{Type: "restore.completed", GameID: gameID, Data: map[string]any{"snapshotId": snapshotID}})
	return preRestore, nil
}

func (c *Coordinator) ReconcileRemote(ctx context.Context) (remote.ReconcileResult, error) {
	r2, err := c.settings.R2(ctx)
	if err != nil {
		c.settings.RecordR2Failure(err)
		return remote.ReconcileResult{}, err
	}
	c.events.PublishContext(ctx, core.Event{Type: "storage.reconcile.started"})
	result, err := c.reconciler.Reconcile(ctx, r2)
	if err != nil {
		c.settings.RecordR2Failure(err)
		return result, err
	}
	c.settings.RecordR2Success()
	c.events.PublishContext(ctx, core.Event{Type: "storage.reconcile.completed", Data: map[string]any{"objects": result.Objects, "snapshots": result.Snapshots, "skipped": result.Skipped, "games": result.Games}})
	slog.Info("R2 snapshot reconciliation completed", "objects", result.Objects, "snapshots", result.Snapshots, "skipped", result.Skipped, "games", result.Games)
	return result, nil
}

func (c *Coordinator) SyncNow(ctx context.Context) (core.SyncResult, error) {
	if !c.syncNowActive.CompareAndSwap(false, true) {
		return core.SyncResult{}, core.ErrSyncInProgress
	}
	defer c.syncNowActive.Store(false)
	if _, err := c.settings.R2(ctx); err != nil {
		c.settings.RecordR2Failure(err)
		return core.SyncResult{}, err
	}
	games, err := c.repository.ListGames(ctx)
	if err != nil {
		return core.SyncResult{}, err
	}
	c.events.PublishContext(ctx, core.Event{Type: "sync.started"})
	result, backupErr := checkpointWatchedGames(ctx, games, c.Backup, func(completed, total int) {
		c.publishSyncProgress(ctx, "checking", completed, total)
	})
	uploaded, uploadErr := c.syncWatchedPending(ctx)
	result.Eligible = uploaded.Eligible
	result.Synced = uploaded.Synced
	result.Failed = uploaded.Failed
	syncErr := errors.Join(backupErr, uploadErr)
	if syncErr == nil {
		c.settings.RecordR2Sync()
	}
	completed := core.Event{Type: "sync.completed", Data: map[string]any{"result": result}}
	if syncErr != nil {
		completed.Message = syncErr.Error()
	}
	c.events.PublishContext(ctx, completed)
	return result, syncErr
}

func (c *Coordinator) SyncInProgress() bool {
	return c.syncNowActive.Load()
}

func (c *Coordinator) publishSyncProgress(ctx context.Context, phase string, completed, total int) {
	c.events.PublishContext(ctx, core.Event{Type: "sync.progress", Data: map[string]any{"phase": phase, "completed": completed, "total": total}})
}

func checkpointWatchedGames(
	ctx context.Context,
	games []core.Game,
	backup func(context.Context, string) (core.Snapshot, error),
	progress func(completed, total int),
) (core.SyncResult, error) {
	var result core.SyncResult
	var failures []error
	total := 0
	for _, game := range games {
		if game.Enabled && game.SyncEnabled && !game.Hidden {
			total++
		}
	}
	if progress != nil {
		progress(0, total)
	}
	for _, game := range games {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		if !game.Enabled || !game.SyncEnabled || game.Hidden {
			continue
		}
		result.CheckedGames++
		_, err := backup(ctx, game.ID)
		switch {
		case err == nil:
			result.CreatedSnapshots++
		case errors.Is(err, snapshot.ErrUnchanged):
			result.UnchangedGames++
		case errors.Is(err, snapshot.ErrNoFiles):
			result.NoFilesGames++
		default:
			result.BackupFailed++
			failures = append(failures, fmt.Errorf("checkpoint %q: %w", game.DisplayName, err))
		}
		if progress != nil {
			progress(result.CheckedGames, total)
		}
	}
	return result, errors.Join(failures...)
}

func (c *Coordinator) syncWatchedPending(ctx context.Context) (core.SyncResult, error) {
	return c.syncPending(ctx, c.periodic.PendingSnapshotsForWatchedGames)
}

func (c *Coordinator) syncPending(ctx context.Context, load func(context.Context) ([]core.Snapshot, error)) (core.SyncResult, error) {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	r2, err := c.settings.R2(ctx)
	if err != nil {
		c.settings.RecordR2Failure(err)
		return core.SyncResult{}, err
	}
	pending, err := load(ctx)
	if err != nil {
		return core.SyncResult{}, err
	}
	result, games, uploadErr := syncSnapshotBatch(pending, func(snapshot core.Snapshot) error {
		return c.syncer.Upload(ctx, r2, snapshot)
	}, func(event core.Event) { c.events.PublishContext(ctx, event) }, func(completed, total int) {
		c.publishSyncProgress(ctx, "uploading", completed, total)
	})
	for gameID := range games {
		if err := c.applyRetention(ctx, gameID); err != nil {
			uploadErr = errors.Join(uploadErr, err)
		}
	}
	if uploadErr != nil {
		c.settings.RecordR2Failure(uploadErr)
	}
	return result, uploadErr
}

func syncSnapshotBatch(snapshots []core.Snapshot, upload func(core.Snapshot) error, publish func(core.Event), progress func(completed, total int)) (core.SyncResult, map[string]struct{}, error) {
	result := core.SyncResult{Eligible: len(snapshots)}
	syncedGames := make(map[string]struct{})
	var uploadErrors []error
	if progress != nil {
		progress(0, len(snapshots))
	}
	for _, snapshot := range snapshots {
		if err := upload(snapshot); err != nil {
			result.Failed++
			uploadErrors = append(uploadErrors, err)
			publish(core.Event{Type: "upload.failed", GameID: snapshot.GameID, Message: err.Error()})
			if progress != nil {
				progress(result.Synced+result.Failed, len(snapshots))
			}
			continue
		}
		result.Synced++
		syncedGames[snapshot.GameID] = struct{}{}
		publish(core.Event{Type: "upload.completed", GameID: snapshot.GameID, Data: map[string]any{"snapshotId": snapshot.ID}})
		if progress != nil {
			progress(result.Synced+result.Failed, len(snapshots))
		}
	}
	return result, syncedGames, errors.Join(uploadErrors...)
}

func (c *Coordinator) SyncGame(ctx context.Context, gameID string) error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return err
	}
	if game.Hidden {
		return core.ErrGameNotFound
	}
	if !game.SyncEnabled {
		return errors.New("R2 sync is disabled for this game")
	}
	r2, err := c.settings.R2(ctx)
	if err != nil {
		c.settings.RecordR2Failure(err)
		return err
	}
	pending, err := c.pending.PendingSnapshotsForGame(ctx, gameID)
	if err != nil {
		return err
	}
	if err := c.uploadAll(ctx, r2, pending); err != nil {
		c.settings.RecordR2Failure(err)
		return err
	}
	return c.applyRetention(ctx, gameID)
}

func (c *Coordinator) automaticSync(ctx context.Context, target core.Snapshot) error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
	game, err := c.repository.Game(ctx, target.GameID)
	if err != nil {
		return err
	}
	if game.Hidden {
		return nil
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
		c.settings.RecordR2Failure(err)
		return err
	}
	for _, target := range excess {
		if target.RemoteState == "synced" && r2 == nil {
			continue
		}
		if target.RemoteState == "synced" {
			key := path.Join("games", target.GameID, "snapshots", target.ID+".json")
			if err := r2.Delete(ctx, key); err != nil {
				c.settings.RecordR2Failure(err)
				return err
			}
			c.settings.RecordR2Success()
		}
		c.backupMu.Lock()
		if err := c.pending.DeleteSnapshot(ctx, target.ID); err != nil {
			c.backupMu.Unlock()
			return err
		}
		c.backupMu.Unlock()
		c.events.PublishContext(ctx, core.Event{Type: "snapshot.retained", GameID: gameID, Data: map[string]any{"deletedSnapshotId": target.ID}})
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
		c.settings.RecordR2Failure(err)
		return err
	}
	c.settings.RecordR2Sync()
	c.events.PublishContext(ctx, core.Event{Type: "upload.completed", GameID: target.GameID, Data: map[string]any{"snapshotId": target.ID}})
	return nil
}

func (c *Coordinator) uploadAll(ctx context.Context, r2 *remote.R2, snapshots []core.Snapshot) error {
	uploaded := false
	for _, pendingSnapshot := range snapshots {
		if err := c.syncer.Upload(ctx, r2, pendingSnapshot); err != nil {
			if uploaded {
				c.settings.RecordR2Sync()
			}
			return err
		}
		uploaded = true
		c.events.PublishContext(ctx, core.Event{Type: "upload.completed", GameID: pendingSnapshot.GameID, Data: map[string]any{"snapshotId": pendingSnapshot.ID}})
	}
	if uploaded {
		c.settings.RecordR2Sync()
	}
	return nil
}

func (c *Coordinator) DeleteSnapshot(ctx context.Context, gameID, snapshotID string, remoteToo bool) error {
	c.syncMu.Lock()
	defer c.syncMu.Unlock()
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
			c.settings.RecordR2Failure(err)
			return fmt.Errorf("load R2 connection before delete: %w", err)
		}
		key := path.Join("games", target.GameID, "snapshots", target.ID+".json")
		if err := r2.Delete(ctx, key); err != nil {
			c.settings.RecordR2Failure(err)
			return err
		}
		c.settings.RecordR2Success()
	}
	c.backupMu.Lock()
	if err := c.pending.DeleteSnapshot(ctx, snapshotID); err != nil {
		c.backupMu.Unlock()
		return err
	}
	c.backupMu.Unlock()
	c.events.PublishContext(ctx, core.Event{Type: "snapshot.deleted", GameID: gameID, Data: map[string]any{"snapshotId": snapshotID, "remote": remoteToo}})
	return nil
}
