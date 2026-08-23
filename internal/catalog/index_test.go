package catalog

import (
	"context"
	"strings"
	"testing"
)

func TestCompileCreatesQueryableStreamingIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	index := Index{Path: t.TempDir() + "/catalog.db"}
	source := `---
Canonical Game:
  files:
    "<home>/Canonical": {}
  installDir:
    CanonicalFolder: {}
  id:
    steamExtra: [42]
  steam:
    id: 41
Alias Game:
  alias: Canonical Game
  steam:
    id: 99
Another Game:
  gog:
    id: 100
`
	if err := Compile(ctx, strings.NewReader(source), index.Path); err != nil {
		t.Fatal(err)
	}
	count, err := index.Count(ctx)
	if err != nil || count != 3 {
		t.Fatalf("unexpected catalog count: count=%d err=%v", count, err)
	}
	for _, id := range []string{"41", "42", "99"} {
		definition, found, err := index.SteamGame(ctx, id)
		if err != nil || !found || definition.Name != "Canonical Game" {
			t.Fatalf("Steam ID %s was not resolved: definition=%#v found=%v err=%v", id, definition, found, err)
		}
	}
	definition, found, err := index.MatchName(ctx, "CanonicalFolder")
	if err != nil || !found || definition.Name != "Canonical Game" {
		t.Fatalf("install directory was not matched: definition=%#v found=%v err=%v", definition, found, err)
	}
	choices, err := index.Search(ctx, "game", 20)
	if err != nil || len(choices) != 2 {
		t.Fatalf("aliases were not excluded from search: choices=%#v err=%v", choices, err)
	}
	definitions := 0
	if err := index.EachDefinition(ctx, func(_ string, _ Definition) error {
		definitions++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if definitions != 2 {
		t.Fatalf("unexpected canonical definition count: %d", definitions)
	}
}

func TestCompileRejectsMalformedEntryWithoutReplacingIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	index := Index{Path: t.TempDir() + "/catalog.db"}
	if err := Compile(ctx, strings.NewReader(testManifest), index.Path); err != nil {
		t.Fatal(err)
	}
	if err := Compile(ctx, strings.NewReader(":"), index.Path); err == nil {
		t.Fatal("malformed catalog was accepted")
	}
	count, err := index.Count(ctx)
	if err != nil || count != 1 {
		t.Fatalf("failed compilation replaced valid index: count=%d err=%v", count, err)
	}
}
