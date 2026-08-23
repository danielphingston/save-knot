package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const maxStoreManifestSize = 2 << 20

type InstalledGame struct {
	Store       string
	StoreID     string
	Name        string
	InstallPath string
	Root        string
	GameDir     string
}

func DiscoverEpic(ctx context.Context, configured []string) ([]InstalledGame, error) {
	directories := append([]string(nil), configured...)
	if programData := os.Getenv("PROGRAMDATA"); programData != "" {
		directories = append(directories, filepath.Join(programData, "Epic", "EpicGamesLauncher", "Data", "Manifests"))
	}
	seen := make(map[string]struct{})
	var games []InstalledGame
	for _, directory := range directories {
		discovered, err := epicGamesInDirectory(ctx, directory)
		if err != nil {
			return nil, err
		}
		for _, game := range discovered {
			if _, ok := seen[game.InstallPath]; ok {
				continue
			}
			seen[game.InstallPath] = struct{}{}
			games = append(games, game)
		}
	}
	sort.Slice(games, func(i, j int) bool { return games[i].Name < games[j].Name })
	return games, nil
}

func epicGamesInDirectory(ctx context.Context, directory string) ([]InstalledGame, error) {
	directory = filepath.Clean(strings.TrimSpace(directory))
	if directory == "." {
		return nil, nil
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read Epic manifest directory %q: %w", directory, err)
	}
	var games []InstalledGame
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".item") {
			continue
		}
		game, err := parseEpicManifest(filepath.Join(directory, entry.Name()))
		if err == nil {
			games = append(games, game)
		}
	}
	return games, nil
}

func parseEpicManifest(manifestPath string) (InstalledGame, error) {
	//nolint:gosec // The path comes from an application-owned Epic manifest directory.
	file, err := os.Open(manifestPath)
	if err != nil {
		return InstalledGame{}, err
	}
	var raw struct {
		AppName         string `json:"AppName"`
		DisplayName     string `json:"DisplayName"`
		InstallLocation string `json:"InstallLocation"`
	}
	data, readErr := io.ReadAll(io.LimitReader(file, maxStoreManifestSize))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return InstalledGame{}, fmt.Errorf("read Epic manifest %q: %w", manifestPath, err)
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return InstalledGame{}, fmt.Errorf("decode Epic manifest %q: %w", manifestPath, err)
	}
	installPath := filepath.Clean(raw.InstallLocation)
	if raw.AppName == "" || raw.DisplayName == "" || !filepath.IsAbs(installPath) {
		return InstalledGame{}, errors.New("epic manifest is missing AppName, DisplayName, or an absolute InstallLocation")
	}
	return InstalledGame{
		Store: "epic", StoreID: raw.AppName, Name: raw.DisplayName, InstallPath: installPath,
		Root: filepath.Dir(installPath), GameDir: filepath.Base(installPath),
	}, nil
}

func DiscoverGOG(ctx context.Context, configured []string) ([]InstalledGame, error) {
	roots := append([]string(nil), configured...)
	if runtime.GOOS == "windows" {
		roots = append(roots,
			filepath.Join(os.Getenv("ProgramFiles(x86)"), "GOG Galaxy", "Games"),
			filepath.Join(os.Getenv("ProgramFiles"), "GOG Galaxy", "Games"),
			`C:\GOG Games`,
		)
	}
	seen := make(map[string]struct{})
	var games []InstalledGame
	for _, root := range roots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "." {
			continue
		}
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read GOG games root %q: %w", root, err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !entry.IsDir() {
				continue
			}
			installPath := filepath.Join(root, entry.Name())
			if _, ok := seen[installPath]; ok {
				continue
			}
			seen[installPath] = struct{}{}
			games = append(games, InstalledGame{Store: "gog", Name: entry.Name(), InstallPath: installPath, Root: root, GameDir: entry.Name()})
		}
	}
	sort.Slice(games, func(i, j int) bool { return games[i].Name < games[j].Name })
	return games, nil
}
