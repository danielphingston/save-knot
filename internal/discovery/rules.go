package discovery

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/saveknot/saveknot/internal/catalog"
	"github.com/saveknot/saveknot/internal/core"
)

// holdsSaves reports whether a Ludusavi file rule may contain save data.
// Rules tagged only as config point at settings, shader and launcher caches;
// backing those up instead of the real saves is worse than no backup at all.
func holdsSaves(rule catalog.FileRule) bool {
	return len(rule.Tags) == 0 || slices.Contains(rule.Tags, "save")
}

// UbisoftRoots returns Ubisoft Connect install directories. Ludusavi models
// Ubisoft cloud saves as <root>/savegames/<storeUserId>/<id>, where <root> is
// the launcher directory even when the game itself was bought on Steam.
func UbisoftRoots() []string {
	candidates := platformUbisoftRoots()
	for _, variable := range []string{"ProgramFiles(x86)", "ProgramFiles"} {
		if base := os.Getenv(variable); base != "" {
			candidates = append(candidates, filepath.Join(base, "Ubisoft", "Ubisoft Game Launcher"))
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	var roots []string
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if info, err := os.Stat(filepath.Join(candidate, "savegames")); err == nil && info.IsDir() {
			roots = append(roots, candidate)
		}
	}
	return roots
}

// ubisoftPaths resolves the definition's Ubisoft Connect save rules against
// each launcher root, skipping resolved paths already present in existing.
func ubisoftPaths(gameID string, definition catalog.Definition, roots []string, existing []core.GamePath) []core.GamePath {
	seen := make(map[string]struct{}, len(existing))
	for _, path := range existing {
		seen[filepath.Clean(path.Resolved)] = struct{}{}
	}
	var paths []core.GamePath
	for template, rule := range definition.Files {
		if !holdsSaves(rule) || !strings.HasPrefix(template, "<root>") || !ruleApplies(rule, "uplay") {
			continue
		}
		for _, root := range roots {
			resolved := filepath.FromSlash(strings.ReplaceAll(strings.Replace(template, "<root>", filepath.ToSlash(root), 1), "<storeUserId>", "*"))
			if strings.Contains(resolved, "<") {
				continue
			}
			matches, err := doublestar.FilepathGlob(resolved)
			if err != nil {
				continue
			}
			for _, match := range matches {
				match = filepath.Clean(match)
				if _, ok := seen[match]; ok {
					continue
				}
				seen[match] = struct{}{}
				paths = append(paths, core.GamePath{
					ID: stableID(gameID, template, match), GameID: gameID, Source: "catalog",
					Template: template, Resolved: match, Enabled: true,
				})
			}
		}
	}
	return paths
}
