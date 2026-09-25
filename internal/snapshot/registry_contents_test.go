package snapshot

import (
	"testing"

	"github.com/saveknot/saveknot/internal/core"
)

func TestSameContentsIncludesRegistrySelection(t *testing.T) {
	base := core.Snapshot{
		Metadata: core.PortableGameMetadata{DisplayName: "Game"},
		Registry: &core.SnapshotRegistry{
			Hash: "identical-export", Keys: []string{`HKEY_CURRENT_USER/Software/Alpha`, `HKEY_CURRENT_USER/Software/Beta`},
		},
	}
	withKeys := func(keys []string) core.Snapshot {
		changed := base
		registry := *base.Registry
		registry.Keys = keys
		changed.Registry = &registry
		return changed
	}
	for _, tc := range []struct {
		name     string
		keys     []string
		wantSame bool
	}{
		{name: "same ordered keys", keys: []string{`HKEY_CURRENT_USER/Software/Alpha`, `HKEY_CURRENT_USER/Software/Beta`}, wantSame: true},
		{name: "different order", keys: []string{`HKEY_CURRENT_USER/Software/Beta`, `HKEY_CURRENT_USER/Software/Alpha`}, wantSame: true},
		{name: "added root with identical export", keys: []string{`HKEY_CURRENT_USER/Software/Alpha`, `HKEY_CURRENT_USER/Software/Beta`, `HKEY_CURRENT_USER/Software/Missing`}},
		{name: "removed root with identical export", keys: []string{`HKEY_CURRENT_USER/Software/Alpha`}},
		{name: "case-insensitive path identity", keys: []string{`HKEY_CURRENT_USER/Software/alpha`, `HKEY_CURRENT_USER/Software/Beta`}, wantSame: true},
		{name: "duplicate root", keys: []string{`HKEY_CURRENT_USER/Software/Alpha`, `HKEY_CURRENT_USER/Software/Beta`, `HKEY_CURRENT_USER/Software/Beta`}, wantSame: true},
		{name: "hive aliases and trailing separators", keys: []string{`HKCU\Software\Beta\`, `HKCU\Software\Alpha/`}, wantSame: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameContents(base, withKeys(tc.keys)); got != tc.wantSame {
				t.Fatalf("sameContents with registry roots %q = %t; want %t", tc.keys, got, tc.wantSame)
			}
		})
	}
	unicodeBase := withKeys([]string{`HKCU\Software\Σ`})
	unicodeChanged := unicodeBase
	registry := *unicodeBase.Registry
	registry.Keys = []string{`HKEY_CURRENT_USER/Software/ς`}
	unicodeChanged.Registry = &registry
	if !sameContents(unicodeBase, unicodeChanged) {
		t.Fatal("registry roots equivalent under strings.EqualFold must compare equally")
	}
	if !sameContents(withKeys(nil), withKeys([]string{})) {
		t.Fatal("nil and empty registry root lists must compare equally")
	}
}
