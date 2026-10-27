package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"
)

// CustomManifest is a user-supplied Ludusavi manifest, such as one of
// Ludusavi's secondary manifests. Its games are merged over the primary
// catalog: new games are added and entries for existing games extend them.
type CustomManifest struct {
	Source string
	Data   []byte
}

// IsManifestURL reports whether a custom manifest source is downloaded rather
// than read from disk.
func IsManifestURL(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

// LoadCustomManifests reads each source as a local file or an HTTP(S) URL. A
// source that cannot be loaded or parsed is skipped and reported in the joined
// error, so one broken manifest never hides the others or the primary catalog.
func LoadCustomManifests(ctx context.Context, client *http.Client, sources []string) ([]CustomManifest, error) {
	if client == nil {
		client = &http.Client{Timeout: 45 * time.Second}
	}
	var manifests []CustomManifest
	var failures []error
	for _, source := range sources {
		data, err := readCustomManifest(ctx, client, source)
		if err == nil {
			_, err = Parse(data)
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("custom manifest %q: %w", source, err))
			continue
		}
		manifests = append(manifests, CustomManifest{Source: source, Data: data})
	}
	return manifests, errors.Join(failures...)
}

func readCustomManifest(ctx context.Context, client *http.Client, source string) ([]byte, error) {
	var reader io.ReadCloser
	if IsManifestURL(source) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != http.StatusOK {
			return nil, errors.Join(fmt.Errorf("unexpected HTTP status %s", response.Status), response.Body.Close())
		}
		reader = response.Body
	} else {
		//nolint:gosec // The user configured this manifest path in their own settings.
		file, err := os.Open(source)
		if err != nil {
			return nil, err
		}
		reader = file
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxManifestSize+1))
	if err := errors.Join(err, reader.Close()); err != nil {
		return nil, err
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("manifest exceeds %d bytes", maxManifestSize)
	}
	return data, nil
}

// customFingerprint identifies the custom manifests an index was built with,
// so the index is rebuilt when they change even if the primary catalog did not.
func customFingerprint(custom []CustomManifest) string {
	if len(custom) == 0 {
		return ""
	}
	hash := sha256.New()
	for _, manifest := range custom {
		hash.Write([]byte(manifest.Source))
		hash.Write([]byte{0})
		hash.Write(manifest.Data)
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// MergeDefinition extends base with the save locations, registry keys, install
// folders and store IDs from addition. Entries from addition win on conflict.
func MergeDefinition(base, addition Definition) Definition {
	base.Files = mergeRules(base.Files, addition.Files)
	base.Registry = mergeRules(base.Registry, addition.Registry)
	if len(addition.InstallDir) > 0 {
		installDirs := make(map[string]any, len(base.InstallDir)+len(addition.InstallDir))
		for directory, value := range base.InstallDir {
			installDirs[directory] = value
		}
		for directory, value := range addition.InstallDir {
			installDirs[directory] = value
		}
		base.InstallDir = installDirs
	}
	if addition.Steam.ID != "" {
		base.Steam = addition.Steam
	}
	if addition.GOG.ID != "" {
		base.GOG = addition.GOG
	}
	base.IDs.SteamExtra = appendMissing(base.IDs.SteamExtra, addition.IDs.SteamExtra)
	base.IDs.GOGExtra = appendMissing(base.IDs.GOGExtra, addition.IDs.GOGExtra)
	if addition.IDs.Lutris != "" {
		base.IDs.Lutris = addition.IDs.Lutris
	}
	return base
}

func mergeRules(base, addition map[string]FileRule) map[string]FileRule {
	if len(addition) == 0 {
		return base
	}
	merged := make(map[string]FileRule, len(base)+len(addition))
	for key, rule := range base {
		merged[key] = rule
	}
	for key, rule := range addition {
		merged[key] = rule
	}
	return merged
}

func appendMissing(values, additions []string) []string {
	for _, addition := range additions {
		if !slices.Contains(values, addition) {
			values = append(values, addition)
		}
	}
	return values
}
