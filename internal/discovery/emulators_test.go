package discovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/catalog"
)

func TestLocalScanAttributesEmulatorSavesBySteamID(t *testing.T) {
	home := t.TempDir()
	public := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PUBLIC", public)
	save := filepath.Join(public, "Documents", "Steam", "RUNE", "782330")
	other := filepath.Join(public, "Documents", "Steam", "RUNE", "Settings")
	for _, directory := range []string{save, other} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	manifest, err := catalog.Parse([]byte(`DOOM Eternal:
  files:
    "<home>/Saved Games/id Software/DOOMEternal/base":
      tags: [save]
  steam:
    id: 782330
`))
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := manifest.Resolve("DOOM Eternal")
	replacements := userPathReplacements(home)
	scan := localScan{replacements: replacements, emulators: indexEmulatorSaves(replacements), now: time.Now()}
	result, ok := scanLocalCandidate(localCandidate{name: "DOOM Eternal", definition: definition}, scan)
	if !ok || len(result.Paths) != 1 || result.Paths[0].Resolved != save {
		t.Fatalf("paths = %#v, want only the RUNE save folder %q", result.Paths, save)
	}
	if result.Paths[0].Template != "<winPublic>/Documents/Steam/RUNE/<storeGameId>" {
		t.Fatalf("template = %q", result.Paths[0].Template)
	}
}

func TestLocalScanAcceptsAbsoluteManifestPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	saves := filepath.Join(t.TempDir(), "Games", "Example", "UniverseLANData")
	if err := os.MkdirAll(saves, 0o700); err != nil {
		t.Fatal(err)
	}
	pattern := filepath.ToSlash(filepath.Join(filepath.Dir(filepath.Dir(saves)), "**", "UniverseLANData"))
	manifest, err := catalog.Parse([]byte("UniverseLAN:\n  files:\n    \"" + pattern + "\": {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	definition, _ := manifest.Resolve("UniverseLAN")
	result, ok := scanLocalCandidate(localCandidate{name: "UniverseLAN", definition: definition}, localScan{replacements: userPathReplacements(home), now: time.Now()})
	if !ok || len(result.Paths) != 1 || result.Paths[0].Resolved != saves {
		t.Fatalf("paths = %#v, want %q", result.Paths, saves)
	}
}
