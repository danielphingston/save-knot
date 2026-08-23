package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/saveknot/saveknot/internal/api"
	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/database"
	"github.com/saveknot/saveknot/internal/events"
	"github.com/saveknot/saveknot/internal/remote"
	"github.com/saveknot/saveknot/internal/secrets"
	"github.com/saveknot/saveknot/internal/settings"
	"github.com/saveknot/saveknot/internal/snapshot"
	"github.com/saveknot/saveknot/internal/watcher"
)

type Application struct {
	database    *database.Store
	coordinator *Coordinator
	server      *api.Server
}

func Build(ctx context.Context, dataDir, listenOverride string) (*Application, error) {
	absoluteDataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	paths := config.DataPaths(absoluteDataDir)
	for _, directory := range []string{paths.Root, paths.Blobs, filepath.Join(paths.Root, "artwork")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create data directory %q: %w", directory, err)
		}
	}
	cfg, err := config.Load(paths.Config)
	if err != nil {
		return nil, err
	}
	if listenOverride != "" {
		cfg.Listen = listenOverride
	}
	if cfg.LocalBackupDir == "" {
		cfg.LocalBackupDir = paths.Blobs
	} else if !filepath.IsAbs(cfg.LocalBackupDir) {
		cfg.LocalBackupDir, err = filepath.Abs(cfg.LocalBackupDir)
		if err != nil {
			return nil, fmt.Errorf("resolve local backup directory: %w", err)
		}
	}
	if err := os.MkdirAll(cfg.LocalBackupDir, 0o700); err != nil {
		return nil, fmt.Errorf("create local backup directory %q: %w", cfg.LocalBackupDir, err)
	}
	settingsManager, err := settings.New(paths.Config, cfg, secrets.Keyring{})
	if err != nil {
		return nil, err
	}
	repository, err := database.Open(ctx, paths.Database)
	if err != nil {
		return nil, err
	}
	watchManager, err := watcher.New()
	if err != nil {
		closeErr := repository.Close()
		return nil, errors.Join(err, closeErr)
	}
	eventBus := events.New()
	snapshotService := snapshot.New(repository, repository, cfg.LocalBackupDir, settingsManager.Config().DeviceID)
	syncer := remote.NewSyncer(repository)
	reconciler := remote.NewReconciler(repository)
	coordinator := NewCoordinator(repository, repository, repository, repository, repository, snapshotService, syncer, reconciler, settingsManager, paths, eventBus, watchManager)
	server, err := api.New(settingsManager.Config().Listen, repository, repository, repository, repository, coordinator, coordinator, coordinator, settingsManager, eventBus, filepath.Join(paths.Root, "artwork"))
	if err != nil {
		return nil, errors.Join(err, watchManager.Close(), repository.Close())
	}
	return &Application{database: repository, coordinator: coordinator, server: server}, nil
}

func (a *Application) Run(ctx context.Context) error {
	a.coordinator.Start(ctx)
	serverErrors := a.server.Start()
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-serverErrors:
		runErr = err
	}
	shutdownContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return errors.Join(runErr, a.server.Close(shutdownContext), a.coordinator.Close(), a.database.Close())
}
