package registrybackup

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var ErrUnsupported = errors.New("windows registry backup is not supported on this operating system")

type Document struct {
	Version int       `json:"version"`
	Keys    []KeyData `json:"keys"`
}

type KeyData struct {
	Path   string      `json:"path"`
	Values []ValueData `json:"values,omitempty"`
}

type ValueData struct {
	Name    string   `json:"name"`
	Type    uint32   `json:"type"`
	String  *string  `json:"string,omitempty"`
	Strings []string `json:"strings,omitempty"`
	Integer *uint64  `json:"integer,omitempty"`
	Binary  []byte   `json:"binary,omitempty"`
}

type Service struct{}

func (Service) Backup(ctx context.Context, roots []string) ([]byte, error) {
	return backup(ctx, roots)
}

func (Service) Restore(ctx context.Context, data []byte, roots []string) error {
	return restore(ctx, data, roots)
}

// validateDocument ensures every registry key in a snapshot is contained by
// one of the roots saved with that snapshot. Registry paths are
// case-insensitive, so comparisons use canonical hive names and folded paths.
func validateDocument(document Document, roots []string) error {
	normalizedRoots, err := normalizeRoots(roots)
	if err != nil {
		return err
	}

	seenKeys := make(map[string]struct{}, len(document.Keys))
	for _, key := range document.Keys {
		if err := validateKeyData(key, normalizedRoots, seenKeys); err != nil {
			return err
		}
	}
	return nil
}

func normalizeRoots(roots []string) ([]string, error) {
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		path, err := normalizePath(root)
		if err != nil {
			return nil, fmt.Errorf("invalid configured registry root %q: %w", root, err)
		}
		normalized = append(normalized, path)
	}
	return normalized, nil
}

// NormalizeRootPaths parses registry roots with the same rules as backup and
// restore validation, while accepting trailing separators for identity checks.
func NormalizeRootPaths(roots []string) ([]string, error) {
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		root = strings.TrimRight(root, `/\`)
		path, err := normalizePath(root)
		if err != nil {
			return nil, fmt.Errorf("invalid configured registry root %q: %w", root, err)
		}
		normalized = append(normalized, path)
	}
	return normalized, nil
}

func validateKeyData(key KeyData, roots []string, seenKeys map[string]struct{}) error {
	normalized, err := normalizePath(key.Path)
	if err != nil {
		return fmt.Errorf("invalid registry key path %q: %w", key.Path, err)
	}
	if !pathWithinRoots(normalized, roots) {
		return fmt.Errorf("registry key path %q is outside configured roots", key.Path)
	}
	folded := strings.ToLower(normalized)
	if _, exists := seenKeys[folded]; exists {
		return fmt.Errorf("duplicate registry key path %q", key.Path)
	}
	seenKeys[folded] = struct{}{}

	seenValues := make(map[string]struct{}, len(key.Values))
	for _, value := range key.Values {
		foldedName := strings.ToLower(value.Name)
		if _, exists := seenValues[foldedName]; exists {
			return fmt.Errorf("duplicate registry value %q in %q", value.Name, key.Path)
		}
		seenValues[foldedName] = struct{}{}
	}
	return nil
}

func pathWithinRoots(path string, roots []string) bool {
	for _, root := range roots {
		if strings.EqualFold(path, root) || strings.HasPrefix(strings.ToLower(path), strings.ToLower(root)+"/") {
			return true
		}
	}
	return false
}

func normalizePath(path string) (string, error) {
	parts := strings.Split(strings.ReplaceAll(path, `\`, "/"), "/")
	if len(parts) < 2 || parts[1] == "" {
		return "", errors.New("path must include a hive and key")
	}
	var hive string
	switch strings.ToUpper(parts[0]) {
	case "HKEY_CURRENT_USER", "HKCU":
		hive = "HKCU"
	case "HKEY_LOCAL_MACHINE", "HKLM":
		hive = "HKLM"
	default:
		return "", fmt.Errorf("unsupported registry hive %q", parts[0])
	}
	for _, part := range parts[1:] {
		if part == "" || part == "." || part == ".." {
			return "", errors.New("path contains an invalid key component")
		}
	}
	return hive + "/" + strings.Join(parts[1:], "/"), nil
}
