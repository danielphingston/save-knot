package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/catalog"
)

func TestSteamParsingAndCatalogResolution(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	library := t.TempDir()
	steamapps := filepath.Join(library, "steamapps")
	if err := os.MkdirAll(steamapps, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `"AppState"
{
  "appid" "1234"
  "name" "Installed Name"
  "installdir" "Example"
}`
	if err := os.WriteFile(filepath.Join(steamapps, "appmanifest_1234.acf"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	game, err := parseAppManifest(filepath.Join(steamapps, "appmanifest_1234.acf"))
	if err != nil {
		t.Fatal(err)
	}
	game.Library = library
	savePath := filepath.Join(library, "steamapps", "common", "Example", "Saves")
	if err := os.MkdirAll(savePath, 0o700); err != nil {
		t.Fatal(err)
	}
	definition := catalog.Definition{Name: "Example Game", Steam: catalog.Store{ID: "1234"}, Files: map[string]catalog.FileRule{"<base>/Saves": {}}}
	resolvedGame, paths, err := CatalogGame(game, definition, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if resolvedGame.DisplayName != "Example Game" || resolvedGame.StoreID != "1234" {
		t.Fatalf("unexpected game: %#v", resolvedGame)
	}
	if len(paths) != 1 || paths[0].Resolved != savePath {
		t.Fatalf("unexpected paths: %#v", paths)
	}
}

func TestDotaSettingsAreFoundOutsideStaleManifestPath(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "steamapps", "common", "dota 2 beta")
	currentConfig := filepath.Join(base, "game", "dota", "cfg")
	accountSettings := filepath.Join(root, "userdata", "12345", "570", "remote")
	for _, path := range []string{currentConfig, accountSettings} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	definition := catalog.Definition{Name: "Dota 2", Files: map[string]catalog.FileRule{"<base>/dota/cfg": {}}}
	_, paths, err := CatalogGame(SteamGame{AppID: "570", InstallDir: "dota 2 beta", Library: root}, definition, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, path := range paths {
		found[path.Resolved] = true
	}
	if !found[currentConfig] || !found[accountSettings] {
		t.Fatalf("Dota settings locations were missed: %#v", paths)
	}
}

func TestParseLibraryFolders(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "libraryfolders.vdf")
	data := `"libraryfolders"
{
  "0" { "path" "/games/one" }
  "path" "/games/two"
}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := parseLibraryFolders(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || paths[0] != filepath.Clean("/games/two") {
		t.Fatalf("unexpected library paths: %#v", paths)
	}
}

func TestSteamPathIdentityFoldsOnlyWindowsPaths(t *testing.T) {
	t.Parallel()
	if steamPathIdentity(`E:\Steam\steamapps\..`, "windows") != steamPathIdentity(`e:/steam`, "windows") {
		t.Fatal("Windows path casing or separators were not folded")
	}
	if steamPathIdentity("/games/Steam", "linux") == steamPathIdentity("/games/steam", "linux") {
		t.Fatal("case-distinct Unix paths were incorrectly folded")
	}
}

func TestDiscoverSteamDeduplicatesAppIDAcrossLibraries(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	createLibrary := func(root, name string) {
		steamapps := filepath.Join(root, "steamapps")
		if err := os.MkdirAll(steamapps, 0o700); err != nil {
			t.Fatal(err)
		}
		manifest := `"AppState"
{
  "appid" "1234"
  "name" "` + name + `"
  "installdir" "Example"
}`
		if err := os.WriteFile(filepath.Join(steamapps, "appmanifest_1234.acf"), []byte(manifest), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first := t.TempDir()
	second := t.TempDir()
	createLibrary(first, "First")
	createLibrary(second, "Second")
	games, err := DiscoverSteam(t.Context(), []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].AppID != "1234" || games[0].Library != first {
		t.Fatalf("duplicate AppID was not resolved deterministically: %#v", games)
	}
}
