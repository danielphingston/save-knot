package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/catalog"
	"github.com/saveknot/saveknot/internal/config"
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
