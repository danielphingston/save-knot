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
	candidates = append(candidates, platformSteamRoots()...)
	home, homeErr := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		candidates = append(candidates,
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "Steam"),
			filepath.Join(os.Getenv("ProgramFiles"), "Steam"),
			`C:\Program Files (x86)\Steam`,
			`C:\Program Files\Steam`,
		)
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
		if strings.TrimSpace(candidate) == "" {
			continue
		}
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
		Image:       fmt.Sprintf("https://shared.cloudflare.steamstatic.com/store_item_assets/steam/apps/%s/library_600x900_2x.jpg", installed.AppID),
		Enabled:     true,
		SyncEnabled: true,
		LastSeen:    &now,
	}
	effective, err := mergeSecondaryManifest(definition, base)
	if err != nil {
		return core.Game{}, nil, err
	}
	paths, err := resolvePaths(identifier, installed, effective, base)
	if err != nil {
		return core.Game{}, nil, err
	}
	return game, paths, nil
}

func mergeSecondaryManifest(primary catalog.Definition, base string) (catalog.Definition, error) {
	secondary, err := catalog.LoadSecondary(filepath.Join(base, ".ludusavi.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return primary, nil
	}
	if err != nil {
		return catalog.Definition{}, err
	}
	var addition catalog.Definition
	if named, ok := secondary.Resolve(primary.Name); ok {
		addition = named
	} else if len(secondary.Games) == 1 {
		for _, only := range secondary.Games {
			addition = only
		}
	} else {
		return catalog.Definition{}, errors.New("secondary Ludusavi manifest does not identify one game")
	}
	files := make(map[string]catalog.FileRule, len(primary.Files)+len(addition.Files))
	for template, rule := range primary.Files {
		files[template] = rule
	}
	for template, rule := range addition.Files {
		files[template] = rule
	}
	primary.Files = files
	registryRules := make(map[string]catalog.FileRule, len(primary.Registry)+len(addition.Registry))
	for key, rule := range primary.Registry {
		registryRules[key] = rule
	}
	for key, rule := range addition.Registry {
		registryRules[key] = rule
	}
	primary.Registry = registryRules
	return primary, nil
}

func resolvePaths(gameID string, installed SteamGame, definition catalog.Definition, base string) ([]core.GamePath, error) {
	return resolveDefinitionPaths(gameID, "steam", installed.Library, installed.InstallDir, installed.AppID, definition, base)
}

func CatalogInstalledGame(installed InstalledGame, definition catalog.Definition, now time.Time) (core.Game, []core.GamePath, error) {
	storeID := installed.StoreID
	if storeID == "" && installed.Store == "gog" {
		storeID = definition.GOG.ID
	}
	identifier := stableID(installed.Store, firstNonEmpty(storeID, definition.Name))
	game := core.Game{
		ID: identifier, CatalogID: definition.Name, CatalogName: definition.Name, DisplayName: definition.Name,
		Store: installed.Store, StoreID: storeID, InstallPath: installed.InstallPath, Enabled: true, SyncEnabled: true, LastSeen: &now,
	}
	effective, err := mergeSecondaryManifest(definition, installed.InstallPath)
	if err != nil {
		return core.Game{}, nil, err
	}
	paths, err := resolveDefinitionPaths(identifier, installed.Store, installed.Root, installed.GameDir, storeID, effective, installed.InstallPath)
	if err != nil {
		return core.Game{}, nil, err
	}
	return game, paths, nil
}

func resolveDefinitionPaths(gameID, store, root, gameDir, storeGameID string, definition catalog.Definition, base string) ([]core.GamePath, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locate home directory: %w", err)
	}
	replacements := userPathReplacements(home)
	replacements["<root>"] = root
	replacements["<game>"] = gameDir
	replacements["<base>"] = base
	replacements["<storeGameId>"] = storeGameID
	var paths []core.GamePath
	for template, rule := range definition.Files {
		if !ruleApplies(rule, store) {
			continue
		}
		resolved := filepath.FromSlash(strings.ReplaceAll(template, "<storeUserId>", "*"))
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func RegistryPaths(gameID, store, storeID, base string, definition catalog.Definition) ([]core.RegistryPath, error) {
	effective, err := mergeSecondaryManifest(definition, base)
	if err != nil {
		return nil, err
	}
	var paths []core.RegistryPath
	for key, rule := range effective.Registry {
		if !ruleApplies(rule, store) {
			continue
		}
		resolved := strings.ReplaceAll(key, "<storeGameId>", storeID)
		if strings.Contains(resolved, "<") {
			continue
		}
		paths = append(paths, core.RegistryPath{
			ID: stableID(gameID, "registry", key), GameID: gameID, Source: "catalog", Path: resolved, Enabled: true,
		})
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
