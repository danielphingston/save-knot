package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/catalog"
)

func TestLocalScanSkipsConfigOnlyRulesAndFindsUbisoftSaves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settings := filepath.Join(home, "Games", "Breakpoint")
	ubisoft := filepath.Join(t.TempDir(), "Ubisoft Game Launcher")
	saves := filepath.Join(ubisoft, "savegames", "user-1", "11903")
	for _, directory := range []string{settings, saves} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := catalog.Parse([]byte(`Breakpoint:
  files:
    "<home>/Games/Breakpoint":
      tags: [config]
    "<root>/savegames/<storeUserId>/11903":
      tags: [save]
      when:
        - store: uplay
`))
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := manifest.Resolve("Breakpoint")
	replacements := userPathReplacements(home)
	result, ok := scanLocalCandidate(localCandidate{name: "Breakpoint", definition: definition}, localScan{replacements: replacements, ubisoftRoots: []string{ubisoft}, now: time.Now()})
	if !ok || len(result.Paths) != 1 || result.Paths[0].Resolved != saves {
		t.Fatalf("paths = %#v, want only Ubisoft save directory %q", result.Paths, saves)
	}
	if _, ok := scanLocalCandidate(localCandidate{name: "Breakpoint", definition: definition}, localScan{replacements: replacements, now: time.Now()}); ok {
		t.Fatal("config-only match was registered as a save location")
	}
}

func TestHoldsSaves(t *testing.T) {
	for _, test := range []struct {
		tags []string
		want bool
	}{{nil, true}, {[]string{"save"}, true}, {[]string{"save", "config"}, true}, {[]string{"config"}, false}} {
		if got := holdsSaves(catalog.FileRule{Tags: test.tags}); got != test.want {
			t.Errorf("holdsSaves(%v) = %v, want %v", test.tags, got, test.want)
		}
	}
}
