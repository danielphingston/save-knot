package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const DefaultManifestURL = "https://raw.githubusercontent.com/mtkennerly/ludusavi-manifest/master/data/manifest.yaml"

type Config struct {
	Listen         string   `json:"listen"`
	ManifestURL    string   `json:"manifestUrl"`
	SteamRoots     []string `json:"steamRoots,omitempty"`
	EpicManifests  []string `json:"epicManifests,omitempty"`
	GOGRoots       []string `json:"gogRoots,omitempty"`
	LocalBackupDir string   `json:"localBackupDir,omitempty"`
	LaunchAtLogin  bool     `json:"launchAtLogin"`
	RetentionKeep  int      `json:"retentionKeep"`
	DeviceID       string   `json:"deviceId"`
	R2             R2       `json:"r2"`
}

type R2 struct {
	AccountID    string `json:"accountId"`
	Bucket       string `json:"bucket"`
	Prefix       string `json:"prefix"`
	AccessKeyID  string `json:"accessKeyId"`
	CredentialID string `json:"credentialId"`
}

type Local struct {
	LocalBackupDir string   `json:"localBackupDir"`
	SteamRoots     []string `json:"steamRoots"`
	EpicManifests  []string `json:"epicManifests"`
	GOGRoots       []string `json:"gogRoots"`
}

type Paths struct {
	Root       string
	Config     string
	Database   string
	Catalog    string
	CatalogTag string
	Blobs      string
}

func DataPaths(root string) Paths {
	return Paths{
		Root:       root,
		Config:     filepath.Join(root, "config.json"),
		Database:   filepath.Join(root, "saveknot.db"),
		Catalog:    filepath.Join(root, "manifest.yaml"),
		CatalogTag: filepath.Join(root, "manifest.etag"),
		Blobs:      filepath.Join(root, "blobs"),
	}
}

func DefaultDataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate user config directory: %w", err)
	}
	return filepath.Join(dir, "SaveKnot"), nil
}

func Load(path string) (Config, error) {
	//nolint:gosec // path is the application-owned config location chosen during bootstrap.
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{Listen: "127.0.0.1:32147", ManifestURL: DefaultManifestURL, RetentionKeep: 50, R2: R2{Prefix: "saveknot"}}, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:32147"
	}
	if cfg.ManifestURL == "" {
		cfg.ManifestURL = DefaultManifestURL
	}
	if cfg.R2.Prefix == "" {
		cfg.R2.Prefix = "saveknot"
	}
	if cfg.RetentionKeep == 0 {
		cfg.RetentionKeep = 50
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	temporary := path + ".new"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func (r R2) Validate() error {
	if strings.TrimSpace(r.AccountID) == "" {
		return errors.New("account ID is required")
	}
	if strings.TrimSpace(r.Bucket) == "" {
		return errors.New("bucket is required")
	}
	if strings.TrimSpace(r.AccessKeyID) == "" {
		return errors.New("access key ID is required")
	}
	if strings.ContainsAny(r.Bucket, "/\\") {
		return errors.New("bucket must be a bucket name, not a path")
	}
	return nil
}

func (r R2) ObjectPrefix() string {
	return strings.Trim(strings.TrimSpace(r.Prefix), "/")
}
