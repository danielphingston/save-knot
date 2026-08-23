package discovery

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/catalog"
)

func TestDeepDiscoveryFindsUserAnchoredSavesOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	save := filepath.Join(home, ".config", "Example", "slot.sav")
	if err := os.MkdirAll(filepath.Dir(save), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(save, []byte("save"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := catalog.Parse([]byte(`Example:
  files:
    "<home>/.config/Example/*.sav": {}
Install only:
  files:
    "<base>/saves": {}
`))
	if err != nil {
		t.Fatal(err)
	}
	found, err := DiscoverLocalSaves(context.Background(), manifest, nil, time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Game.DisplayName != "Example" || len(found[0].Paths) != 1 || found[0].Paths[0].Resolved != save {
		t.Fatalf("unexpected deep discovery result: %#v", found)
	}
}
