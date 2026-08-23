package discovery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverEpicReadsItemManifests(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	install := filepath.Join(root, "Installed", "Epic Game")
	manifestDir := filepath.Join(root, "Manifests")
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"AppName":"epic-id","DisplayName":"Epic Game","InstallLocation":%q}`, install)
	if err := os.WriteFile(filepath.Join(manifestDir, "game.item"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	games, err := DiscoverEpic(context.Background(), []string{manifestDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].StoreID != "epic-id" || games[0].InstallPath != install {
		t.Fatalf("unexpected Epic discovery: %#v", games)
	}
}

func TestDiscoverGOGUsesConfiguredRoots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "GOG Game"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "not-a-game.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	games, err := DiscoverGOG(context.Background(), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 || games[0].Name != "GOG Game" || games[0].Root != root {
		t.Fatalf("unexpected GOG discovery: %#v", games)
	}
}
