package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const customPrimary = `Shared Game:
  files:
    "<home>/Shared/save.dat":
      tags: [save]
  steam:
    id: 10
`

func TestCompileMergesCustomManifests(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	index := Index{Path: filepath.Join(t.TempDir(), "catalog.db")}
	custom := CustomManifest{Source: "custom.yaml", Data: []byte(`Shared Game:
  files:
    "<home>/Shared/extra.dat":
      tags: [save]
  installDir:
    SharedFolder: {}
Homebrew Game:
  files:
    "<home>/Homebrew/*.sav": {}
  steam:
    id: 20
`)}
	if err := Compile(ctx, strings.NewReader(customPrimary), index.Path, custom); err != nil {
		t.Fatal(err)
	}
	shared, found, err := index.SteamGame(ctx, "10")
	if err != nil || !found {
		t.Fatalf("shared game lookup: found=%v err=%v", found, err)
	}
	if len(shared.Files) != 2 {
		t.Fatalf("shared game files = %v, want primary and custom entries", shared.Files)
	}
	if byDir, found, err := index.MatchName(ctx, "SharedFolder"); err != nil || !found || byDir.Name != "Shared Game" {
		t.Fatalf("custom install folder lookup: %#v found=%v err=%v", byDir, found, err)
	}
	if homebrew, found, err := index.SteamGame(ctx, "20"); err != nil || !found || homebrew.Name != "Homebrew Game" {
		t.Fatalf("custom game lookup: %#v found=%v err=%v", homebrew, found, err)
	}
	if count, err := index.Count(ctx); err != nil || count != 2 {
		t.Fatalf("count = %d err=%v, want 2", count, err)
	}
}

func TestEnsureIndexRebuildsWhenCustomManifestsChange(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	manifestPath := filepath.Join(root, "manifest.yaml")
	index := Index{Path: filepath.Join(root, "catalog.db")}
	if err := os.WriteFile(manifestPath, []byte(customPrimary), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureIndex(ctx, manifestPath, index.Path); err != nil {
		t.Fatal(err)
	}
	custom := CustomManifest{Source: "custom.yaml", Data: []byte("Added Game:\n  files:\n    \"<home>/Added\": {}\n")}
	if err := EnsureIndex(ctx, manifestPath, index.Path, custom); err != nil {
		t.Fatal(err)
	}
	if _, found, err := index.Resolve(ctx, "Added Game"); err != nil || !found {
		t.Fatalf("custom game missing after rebuild: found=%v err=%v", found, err)
	}
	if err := EnsureIndex(ctx, manifestPath, index.Path); err != nil {
		t.Fatal(err)
	}
	if _, found, err := index.Resolve(ctx, "Added Game"); err != nil || found {
		t.Fatalf("removed custom manifest still indexed: found=%v err=%v", found, err)
	}
}

func TestLoadCustomManifestsSkipsBrokenSources(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if _, err := writer.Write([]byte("Remote Game:\n  files:\n    \"<home>/Remote\": {}\n")); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	broken := filepath.Join(t.TempDir(), "broken.yaml")
	if err := os.WriteFile(broken, []byte("not: [valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCustomManifests(context.Background(), server.Client(), []string{server.URL, broken, filepath.Join(t.TempDir(), "missing.yaml")})
	if len(loaded) != 1 || loaded[0].Source != server.URL {
		t.Fatalf("loaded = %#v, want only the remote manifest", loaded)
	}
	if err == nil || !strings.Contains(err.Error(), "broken.yaml") || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("error = %v, want both failures reported", err)
	}
}
