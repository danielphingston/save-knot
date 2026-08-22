package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const maxManifestSize = 96 << 20

type Definition struct {
	Name       string
	Files      map[string]FileRule `yaml:"files"`
	Registry   map[string]FileRule `yaml:"registry"`
	InstallDir map[string]any      `yaml:"installDir"`
	Steam      Store               `yaml:"steam"`
	GOG        Store               `yaml:"gog"`
	Aliases    []string            `yaml:"aliases"`
}

type FileRule struct {
	Tags []string    `yaml:"tags"`
	When []Condition `yaml:"when"`
}

type Condition struct {
	OS    string `yaml:"os"`
	Store string `yaml:"store"`
}

type Store struct {
	ID string `yaml:"-"`
}

func (s *Store) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == 0 {
		return nil
	}
	var raw struct {
		ID yaml.Node `yaml:"id"`
	}
	if err := node.Decode(&raw); err != nil {
		return fmt.Errorf("decode store definition: %w", err)
	}
	if raw.ID.Kind != 0 {
		s.ID = raw.ID.Value
	}
	return nil
}

type Manifest struct {
	Games   map[string]Definition
	BySteam map[string]string
}

func Parse(data []byte) (*Manifest, error) {
	definitions := make(map[string]Definition)
	if err := yaml.Unmarshal(data, &definitions); err != nil {
		return nil, fmt.Errorf("decode Ludusavi manifest: %w", err)
	}
	bySteam := make(map[string]string)
	for name, definition := range definitions {
		definition.Name = name
		definitions[name] = definition
		if definition.Steam.ID != "" {
			bySteam[definition.Steam.ID] = name
		}
	}
	return &Manifest{Games: definitions, BySteam: bySteam}, nil
}

func Load(path string) (*Manifest, error) {
	// path is the application-owned catalog cache selected during bootstrap.
	//nolint:gosec // The caller cannot influence this path through the local HTTP API.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Ludusavi manifest: %w", err)
	}
	return Parse(data)
}

func (m *Manifest) SteamGame(id string) (Definition, bool) {
	name, ok := m.BySteam[id]
	if !ok {
		return Definition{}, false
	}
	definition, ok := m.Games[name]
	return definition, ok
}

type Fetcher struct {
	Client   *http.Client
	URL      string
	Path     string
	ETagPath string
}

func (f Fetcher) Update(ctx context.Context) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return false, fmt.Errorf("create manifest request: %w", err)
	}
	etag, err := os.ReadFile(f.ETagPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read manifest ETag: %w", err)
	}
	if len(etag) > 0 {
		request.Header.Set("If-None-Match", strings.TrimSpace(string(etag)))
	}
	client := f.Client
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	response, err := client.Do(request)
	if err != nil {
		return false, fmt.Errorf("download Ludusavi manifest: %w", err)
	}
	if response.StatusCode == http.StatusNotModified {
		if err := response.Body.Close(); err != nil {
			return false, fmt.Errorf("close manifest response: %w", err)
		}
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		closeErr := response.Body.Close()
		return false, errors.Join(fmt.Errorf("download Ludusavi manifest: unexpected HTTP status %s", response.Status), closeErr)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxManifestSize+1))
	closeErr := response.Body.Close()
	if err := errors.Join(err, closeErr); err != nil {
		return false, fmt.Errorf("read Ludusavi manifest: %w", err)
	}
	if len(data) > maxManifestSize {
		return false, fmt.Errorf("ludusavi manifest exceeds %d bytes", maxManifestSize)
	}
	if _, err := Parse(data); err != nil {
		return false, err
	}
	if err := writeAtomic(f.Path, data, 0o600); err != nil {
		return false, err
	}
	if tag := response.Header.Get("ETag"); tag != "" {
		if err := writeAtomic(f.ETagPath, []byte(tag+"\n"), 0o600); err != nil {
			return false, err
		}
	}
	return true, nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	temporary := path + ".new"
	if err := os.WriteFile(temporary, data, mode); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace %q: %w", path, err)
	}
	return nil
}
