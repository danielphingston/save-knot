package discovery

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/saveknot/saveknot/internal/catalog"
	"github.com/saveknot/saveknot/internal/core"
)

var (
	vdfPairPattern = regexp.MustCompile(`^\s*"([^"]+)"\s+"(.*)"\s*$`)
	manifestFields = regexp.MustCompile(`(?m)^\s*"(appid|name|installdir)"\s+"([^"]*)"`)
)

type SteamGame struct {
	AppID      string
	Name       string
	InstallDir string
	Library    string
}

func SteamRoots(configured []string) []string {
	seen := make(map[string]struct{})
	var candidates []string
	candidates = append(candidates, configured...)
	home, homeErr := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		candidates = append(candidates, `C:\Program Files (x86)\Steam`, `C:\Program Files\Steam`)
	case "darwin":
		if homeErr == nil {
			candidates = append(candidates, filepath.Join(home, "Library/Application Support/Steam"))
		}
	default:
		if homeErr == nil {
			candidates = append(candidates,
				filepath.Join(home, ".steam/steam"),
				filepath.Join(home, ".local/share/Steam"),
				filepath.Join(home, ".var/app/com.valvesoftware.Steam/data/Steam"),
			)
		}
	}
	var roots []string
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		if info, err := os.Stat(filepath.Join(candidate, "steamapps")); err == nil && info.IsDir() {
			seen[candidate] = struct{}{}
			roots = append(roots, candidate)
		}
	}
	return roots
}

func DiscoverSteam(ctx context.Context, roots []string) ([]SteamGame, error) {
	libraries := make(map[string]struct{})
	for _, root := range SteamRoots(roots) {
		libraries[root] = struct{}{}
		paths, err := parseLibraryFolders(filepath.Join(root, "steamapps", "libraryfolders.vdf"))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, path := range paths {
			libraries[path] = struct{}{}
		}
	}
	var games []SteamGame
	for library := range libraries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		manifests, err := filepath.Glob(filepath.Join(library, "steamapps", "appmanifest_*.acf"))
		if err != nil {
			return nil, fmt.Errorf("find Steam app manifests: %w", err)
		}
		for _, path := range manifests {
			game, err := parseAppManifest(path)
			if err != nil {
				continue
			}
			game.Library = library
			games = append(games, game)
		}
	}
	sort.Slice(games, func(i, j int) bool { return games[i].Name < games[j].Name })
	return games, nil
}

func parseLibraryFolders(path string) (paths []string, err error) {
	//nolint:gosec // path is derived from a detected Steam installation root.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		match := vdfPairPattern.FindStringSubmatch(scanner.Text())
		if len(match) == 3 && match[1] == "path" {
			paths = append(paths, filepath.Clean(strings.ReplaceAll(match[2], `\\`, `\`)))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read Steam library list: %w", err)
	}
	return paths, nil
}

func parseAppManifest(path string) (SteamGame, error) {
	//nolint:gosec // path is returned by globbing inside a detected Steam library.
	data, err := os.ReadFile(path)
	if err != nil {
		return SteamGame{}, fmt.Errorf("read Steam app manifest: %w", err)
	}
	values := make(map[string]string)
	for _, match := range manifestFields.FindAllStringSubmatch(string(data), -1) {
		values[match[1]] = match[2]
	}
	if values["appid"] == "" || values["installdir"] == "" {
		return SteamGame{}, errors.New("steam app manifest is missing appid or installdir")
	}
	return SteamGame{AppID: values["appid"], Name: values["name"], InstallDir: values["installdir"]}, nil
}

func CatalogGame(installed SteamGame, definition catalog.Definition, now time.Time) (core.Game, []core.GamePath, error) {
	identifier := stableID("steam", installed.AppID)
	base := filepath.Join(installed.Library, "steamapps", "common", installed.InstallDir)
	game := core.Game{
		ID:          identifier,
		CatalogID:   definition.Name,
		CatalogName: definition.Name,
		DisplayName: definition.Name,
		Store:       "steam",
		StoreID:     installed.AppID,
		InstallPath: base,
		Enabled:     true,
		LastSeen:    &now,
	}
	paths, err := resolvePaths(identifier, installed, definition, base)
	if err != nil {
		return core.Game{}, nil, err
	}
	return game, paths, nil
}

func resolvePaths(gameID string, installed SteamGame, definition catalog.Definition, base string) ([]core.GamePath, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home directory: %w", err)
	}
	replacements := map[string]string{
		"<root>":            installed.Library,
		"<game>":            installed.InstallDir,
		"<base>":            base,
		"<home>":            home,
		"<storeGameId>":     installed.AppID,
		"<osUserName>":      filepath.Base(home),
		"<winAppData>":      os.Getenv("APPDATA"),
		"<winLocalAppData>": os.Getenv("LOCALAPPDATA"),
		"<winDocuments>":    filepath.Join(home, "Documents"),
		"<winPublic>":       os.Getenv("PUBLIC"),
		"<winProgramData>":  os.Getenv("PROGRAMDATA"),
		"<winDir>":          os.Getenv("WINDIR"),
		"<xdgData>":         xdgPath("XDG_DATA_HOME", filepath.Join(home, ".local", "share")),
		"<xdgConfig>":       xdgPath("XDG_CONFIG_HOME", filepath.Join(home, ".config")),
	}
	var paths []core.GamePath
	for template, rule := range definition.Files {
		if !ruleApplies(rule, "steam") {
			continue
		}
		resolved := filepath.FromSlash(template)
		for placeholder, value := range replacements {
			if value != "" {
				resolved = strings.ReplaceAll(resolved, placeholder, value)
			}
		}
		if strings.Contains(resolved, "<") || !filepath.IsAbs(resolved) {
			continue
		}
		matches, err := doublestar.FilepathGlob(resolved)
		if err != nil {
			return nil, fmt.Errorf("resolve save path %q: %w", template, err)
		}
		if len(matches) == 0 && !hasGlob(resolved) {
			matches = []string{resolved}
		}
		for _, match := range matches {
			paths = append(paths, core.GamePath{
				ID: stableID(gameID, template, match), GameID: gameID, Source: "catalog",
				Template: template, Resolved: filepath.Clean(match), Enabled: true,
			})
		}
	}
	return paths, nil
}

func ruleApplies(rule catalog.FileRule, store string) bool {
	if len(rule.When) == 0 {
		return true
	}
	for _, condition := range rule.When {
		osMatches := condition.OS == "" || condition.OS == runtime.GOOS || condition.OS == "mac" && runtime.GOOS == "darwin"
		if osMatches && (condition.Store == "" || condition.Store == store) {
			return true
		}
	}
	return false
}

func stableID(parts ...string) string {
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(hash[:12])
}

func xdgPath(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func hasGlob(path string) bool {
	return strings.ContainsAny(path, "*?[")
}
