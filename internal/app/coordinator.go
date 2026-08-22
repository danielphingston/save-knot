package app

import (
	"context"
	"errors"
	"fmt"
	"os"
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
}

type Coordinator struct {
	repository  repository
	pending     pendingRepository
	snapshots   *snapshot.Service
	syncer      *remote.Syncer
	settings    *settings.Manager
	paths       config.Paths
	events      *events.Bus
	watcher     *watcher.Manager
	backupMu    sync.Mutex
	reconcileMu sync.Mutex
}

func NewCoordinator(
	repository repository,
	pending pendingRepository,
	snapshots *snapshot.Service,
	syncer *remote.Syncer,
	settings *settings.Manager,
	paths config.Paths,
	eventBus *events.Bus,
	watchManager *watcher.Manager,
) *Coordinator {
	return &Coordinator{
		repository: repository, pending: pending, snapshots: snapshots, syncer: syncer, settings: settings,
		paths: paths, events: eventBus, watcher: watchManager,
	}
}

func (c *Coordinator) Start(ctx context.Context) {
	c.watcher.Start(ctx, func(jobContext context.Context, gameID string) {
		if _, err := c.Backup(jobContext, gameID); err != nil && !errors.Is(err, snapshot.ErrNoFiles) {
			c.events.Publish(core.Event{Type: "snapshot.failed", GameID: gameID, Message: err.Error()})
		}
	})
	go c.run(ctx)
}

func (c *Coordinator) Close() error {
	return c.watcher.Close()
}

func (c *Coordinator) run(ctx context.Context) {
	c.refresh(ctx)
	catalogTicker := time.NewTicker(24 * time.Hour)
	discoveryTicker := time.NewTicker(5 * time.Minute)
	defer catalogTicker.Stop()
	defer discoveryTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-catalogTicker.C:
			c.refresh(ctx)
		case <-discoveryTicker.C:
			c.discover(ctx)
		}
	}
}

func (c *Coordinator) refresh(ctx context.Context) {
	cfg := c.settings.Config()
	fetcher := catalog.Fetcher{URL: cfg.ManifestURL, Path: c.paths.Catalog, ETagPath: c.paths.CatalogTag}
	updated, err := fetcher.Update(ctx)
	if err != nil {
		if _, statErr := os.Stat(c.paths.Catalog); statErr != nil {
			c.events.Publish(core.Event{Type: "catalog.failed", Message: err.Error()})
			return
		}
	} else if updated {
		c.events.Publish(core.Event{Type: "catalog.updated"})
	}
	c.discover(ctx)
}

func (c *Coordinator) discover(ctx context.Context) {
	c.reconcileMu.Lock()
	defer c.reconcileMu.Unlock()
	manifest, err := catalog.Load(c.paths.Catalog)
	if err != nil {
		return
	}
	installed, err := discovery.DiscoverSteam(ctx, c.settings.Config().SteamRoots)
	if err != nil {
		c.events.Publish(core.Event{Type: "discovery.failed", Message: err.Error()})
		return
	}
	now := time.Now().UTC()
	for _, installation := range installed {
		definition, ok := manifest.SteamGame(installation.AppID)
		if !ok {
			continue
		}
		game, paths, err := discovery.CatalogGame(installation, definition, now)
		if err != nil {
			continue
		}
		if err := c.repository.UpsertGame(ctx, game); err != nil {
			continue
		}
		if err := c.repository.ReplaceCatalogPaths(ctx, game.ID, paths); err != nil {
			continue
		}
		c.events.Publish(core.Event{Type: "game.discovered", GameID: game.ID})
	}
	c.ReconcileWatches(ctx)
}

func (c *Coordinator) ReconcileWatches(ctx context.Context) {
	games, err := c.repository.ListGames(ctx)
	if err != nil {
		return
	}
	var targets []watcher.Target
	for _, game := range games {
		if !game.Enabled {
			continue
		}
		paths, err := c.repository.GamePaths(ctx, game.ID)
		if err != nil {
			continue
		}
		for _, path := range paths {
			if path.Enabled {
				targets = append(targets, watcher.Target{GameID: game.ID, Path: path.Resolved})
			}
		}
	}
	c.watcher.Reconcile(targets)
}

func (c *Coordinator) Backup(ctx context.Context, gameID string) (core.Snapshot, error) {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return core.Snapshot{}, err
	}
	if !game.Enabled {
		return core.Snapshot{}, errors.New("sync is disabled for this game")
	}
	c.events.Publish(core.Event{Type: "snapshot.started", GameID: gameID})
	created, err := c.snapshots.Create(ctx, game)
	if err != nil {
		return core.Snapshot{}, err
	}
	r2, err := c.settings.R2(ctx)
	if errors.Is(err, settings.ErrR2NotConfigured) {
		c.events.Publish(core.Event{Type: "snapshot.completed", GameID: gameID, Data: map[string]any{"snapshotId": created.ID, "remote": false}})
		return created, nil
	}
	if err != nil {
		return created, fmt.Errorf("load R2 connection: %w", err)
	}
	if err := c.syncer.Upload(ctx, r2, created); err != nil {
		return created, err
	}
	created.RemoteState = "synced"
	c.events.Publish(core.Event{Type: "snapshot.completed", GameID: gameID, Data: map[string]any{"snapshotId": created.ID, "remote": true}})
	return created, nil
}

func (c *Coordinator) Restore(ctx context.Context, gameID, snapshotID string) (core.Snapshot, error) {
	c.backupMu.Lock()
	defer c.backupMu.Unlock()
	game, err := c.repository.Game(ctx, gameID)
	if err != nil {
		return core.Snapshot{}, err
	}
	c.events.Publish(core.Event{Type: "restore.started", GameID: gameID})
	preRestore, err := c.snapshots.Restore(ctx, game, snapshotID)
	if err != nil {
		return core.Snapshot{}, err
	}
	c.events.Publish(core.Event{Type: "restore.completed", GameID: gameID, Data: map[string]any{"snapshotId": snapshotID}})
	return preRestore, nil
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
	for _, pendingSnapshot := range pending {
		if err := c.syncer.Upload(ctx, r2, pendingSnapshot); err != nil {
			return err
		}
		c.events.Publish(core.Event{Type: "upload.completed", GameID: pendingSnapshot.GameID, Data: map[string]any{"snapshotId": pendingSnapshot.ID}})
	}
	return nil
}
