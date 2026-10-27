package discovery

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/saveknot/saveknot/internal/catalog"
	"github.com/saveknot/saveknot/internal/core"
)

// emulatorSaveRoots are folders where common Steam emulators and cracks keep
// saves, one subfolder per Steam app ID (for example RUNE\782330). Ludusavi's
// manifest only lists the real game's own save folders, so these are matched
// to games by the catalog's Steam IDs instead.
var emulatorSaveRoots = []string{
	"<winPublic>/Documents/Steam/RUNE",
	"<winPublic>/Documents/Steam/CODEX",
	"<winAppData>/Steam/CODEX",
	"<winPublic>/Documents/OnlineFix",
	"<winAppData>/EMPRESS",
	"<winPublic>/EMPRESS",
	"<winAppData>/GSE Saves",
	"<winAppData>/Goldberg SteamEmu Saves",
	"<winAppData>/SmartSteamEmu",
	"<winDocuments>/SkidRow",
	"<winLocalAppData>/SKIDROW",
	"<winAppData>/FLT",
	"<winAppData>/.1911",
}

type emulatorSave struct {
	template string
	resolved string
}

// emulatorSaves maps a Steam app ID to the emulator save folders found for it.
type emulatorSaves map[string][]emulatorSave

// indexEmulatorSaves lists each emulator root once, so matching thousands of
// catalog games costs a map lookup each rather than a file system check.
func indexEmulatorSaves(replacements map[string]string) emulatorSaves {
	saves := make(emulatorSaves)
	seen := make(map[string]struct{})
	for _, template := range emulatorSaveRoots {
		root := filepath.FromSlash(template)
		for placeholder, value := range replacements {
			if value != "" {
				root = strings.ReplaceAll(root, placeholder, value)
			}
		}
		if strings.Contains(root, "<") || !filepath.IsAbs(root) {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || !steamAppID(entry.Name()) {
				continue
			}
			resolved := filepath.Join(root, entry.Name())
			// Windows paths are case-insensitive, so RUNE and Rune can be one folder.
			if _, ok := seen[strings.ToLower(resolved)]; ok {
				continue
			}
			seen[strings.ToLower(resolved)] = struct{}{}
			saves[entry.Name()] = append(saves[entry.Name()], emulatorSave{template: path.Join(template, "<storeGameId>"), resolved: resolved})
		}
	}
	return saves
}

func steamAppID(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range name {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// paths returns the emulator save folders for the definition's Steam IDs that
// are not already among existing.
func (s emulatorSaves) paths(gameID string, definition catalog.Definition, existing []core.GamePath) []core.GamePath {
	if len(s) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(existing))
	for _, existingPath := range existing {
		seen[filepath.Clean(existingPath.Resolved)] = struct{}{}
	}
	var paths []core.GamePath
	for _, id := range append([]string{definition.Steam.ID}, definition.IDs.SteamExtra...) {
		for _, save := range s[id] {
			if _, ok := seen[save.resolved]; ok {
				continue
			}
			seen[save.resolved] = struct{}{}
			paths = append(paths, core.GamePath{
				ID: stableID(gameID, save.template, save.resolved), GameID: gameID, Source: "catalog",
				Template: save.template, Resolved: save.resolved, Enabled: true,
			})
		}
	}
	return paths
}
