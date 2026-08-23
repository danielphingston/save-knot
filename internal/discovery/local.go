package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/saveknot/saveknot/internal/catalog"
	"github.com/saveknot/saveknot/internal/core"
)

const localScanWorkers = 8

type localCandidate struct {
	name       string
	definition catalog.Definition
}

type LocalResult struct {
	Game  core.Game
	Paths []core.GamePath
}

func DiscoverLocalSaves(ctx context.Context, manifest *catalog.Manifest, excluded map[string]struct{}, now time.Time) ([]LocalResult, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	replacements := userPathReplacements(home)
	jobs, results := startLocalWorkers(ctx, len(manifest.Games), replacements, now)
	go feedLocalCandidates(ctx, manifest, excluded, jobs)
	var found []LocalResult
	for result := range results {
		found = append(found, result)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Game.DisplayName < found[j].Game.DisplayName })
	return found, nil
}

func startLocalWorkers(ctx context.Context, games int, replacements map[string]string, now time.Time) (chan<- localCandidate, <-chan LocalResult) {
	jobs := make(chan localCandidate)
	results := make(chan LocalResult)
	workers := min(localScanWorkers, games)
	var wait sync.WaitGroup
	wait.Add(workers)
	for range workers {
		go localWorker(ctx, jobs, results, replacements, now, &wait)
	}
	go func() {
		wait.Wait()
		close(results)
	}()
	return jobs, results
}

func localWorker(ctx context.Context, jobs <-chan localCandidate, results chan<- LocalResult, replacements map[string]string, now time.Time, wait *sync.WaitGroup) {
	defer wait.Done()
	for candidate := range jobs {
		result, ok := scanLocalCandidate(candidate, replacements, now)
		if !ok {
			continue
		}
		select {
		case results <- result:
		case <-ctx.Done():
			return
		}
	}
}

func feedLocalCandidates(ctx context.Context, manifest *catalog.Manifest, excluded map[string]struct{}, jobs chan<- localCandidate) {
	defer close(jobs)
	for name, definition := range manifest.Games {
		if definition.Alias != "" {
			continue
		}
		if _, skip := excluded[name]; skip {
			continue
		}
		select {
		case jobs <- localCandidate{name: name, definition: definition}:
		case <-ctx.Done():
			return
		}
	}
}

func scanLocalCandidate(candidate localCandidate, replacements map[string]string, now time.Time) (LocalResult, bool) {
	gameID := stableID("local", candidate.name)
	seen := make(map[string]struct{})
	var paths []core.GamePath
	for template, rule := range candidate.definition.Files {
		matches := localMatches(template, rule, replacements)
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
	if len(paths) == 0 {
		return LocalResult{}, false
	}
	game := core.Game{
		ID: gameID, CatalogID: candidate.name, CatalogName: candidate.name, DisplayName: candidate.name,
		Store: "local", Enabled: true, SyncEnabled: true, LastSeen: &now,
	}
	return LocalResult{Game: game, Paths: paths}, true
}

func localMatches(template string, rule catalog.FileRule, replacements map[string]string) []string {
	if !localRuleApplies(rule) || !userAnchored(template) {
		return nil
	}
	resolved := filepath.FromSlash(template)
	for placeholder, value := range replacements {
		resolved = strings.ReplaceAll(resolved, placeholder, value)
	}
	if strings.Contains(resolved, "<") || !filepath.IsAbs(resolved) || !globParentExists(resolved) {
		return nil
	}
	matches, err := doublestar.FilepathGlob(resolved)
	if err != nil {
		return nil
	}
	if len(matches) == 0 && !hasGlob(resolved) {
		if _, err := os.Stat(resolved); err == nil {
			return []string{resolved}
		}
	}
	return matches
}

func userPathReplacements(home string) map[string]string {
	return map[string]string{
		"<home>":               home,
		"<osUserName>":         filepath.Base(home),
		"<winAppData>":         os.Getenv("APPDATA"),
		"<winLocalAppData>":    os.Getenv("LOCALAPPDATA"),
		"<winLocalAppDataLow>": filepath.Join(home, "AppData", "LocalLow"),
		"<winDocuments>":       filepath.Join(home, "Documents"),
		"<winPublic>":          os.Getenv("PUBLIC"),
		"<winProgramData>":     os.Getenv("PROGRAMDATA"),
		"<winDir>":             os.Getenv("WINDIR"),
		"<xdgData>":            xdgPath("XDG_DATA_HOME", filepath.Join(home, ".local", "share")),
		"<xdgConfig>":          xdgPath("XDG_CONFIG_HOME", filepath.Join(home, ".config")),
	}
}

func userAnchored(template string) bool {
	for _, placeholder := range []string{
		"<home>", "<winAppData>", "<winLocalAppData>", "<winLocalAppDataLow>", "<winDocuments>",
		"<winPublic>", "<winProgramData>", "<winDir>", "<xdgData>", "<xdgConfig>",
	} {
		if strings.HasPrefix(template, placeholder) {
			return true
		}
	}
	return false
}

func localRuleApplies(rule catalog.FileRule) bool {
	if len(rule.When) == 0 {
		return true
	}
	for _, condition := range rule.When {
		if condition.OS == "" || condition.OS == runtime.GOOS || condition.OS == "mac" && runtime.GOOS == "darwin" {
			return true
		}
	}
	return false
}

func globParentExists(pattern string) bool {
	firstGlob := strings.IndexAny(pattern, "*?[")
	literal := pattern
	if firstGlob >= 0 {
		literal = pattern[:firstGlob]
	}
	literal = strings.TrimRight(literal, string(filepath.Separator))
	if literal == "" {
		return false
	}
	info, err := os.Stat(literal)
	if err == nil {
		return info.IsDir() || info.Mode().IsRegular()
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	info, err = os.Stat(filepath.Dir(literal))
	return err == nil && info.IsDir()
}
