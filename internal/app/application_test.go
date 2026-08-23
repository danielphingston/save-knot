package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
	application, err := Build(ctx, dataDir, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(application.coordinator.Close(), application.database.Close()); err != nil {
			t.Error(err)
		}
	})

	application.coordinator.discover(ctx, true)
	diagnostics := application.coordinator.Diagnostics()
	if !diagnostics.Catalog.Loaded || diagnostics.Catalog.GameCount != 1 {
		t.Fatalf("catalog did not become ready: %#v", diagnostics)
	}
}
