package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const DefaultManifestURL = "https://raw.githubusercontent.com/mtkennerly/ludusavi-manifest/master/data/manifest.yaml"

type Config struct {
	Listen         string     `json:"listen"`
	ManifestURL    string     `json:"manifestUrl"`
	SteamRoots     []string   `json:"steamRoots,omitempty"`
	EpicManifests  []string   `json:"epicManifests,omitempty"`
	GOGRoots       []string   `json:"gogRoots,omitempty"`
	LocalBackupDir string     `json:"localBackupDir,omitempty"`
	LaunchAtLogin  bool       `json:"launchAtLogin"`
	RetentionKeep  int        `json:"retentionKeep"`
	Automation     Automation `json:"automation"`
	DeviceID       string     `json:"deviceId"`
	R2             R2         `json:"r2"`
}

type Automation struct {
	PeriodicSyncEnabled      bool `json:"periodicSyncEnabled"`
	SyncIntervalMinutes      int  `json:"syncIntervalMinutes"`
	PeriodicDiscoveryEnabled bool `json:"periodicDiscoveryEnabled"`
	DiscoveryIntervalMinutes int  `json:"discoveryIntervalMinutes"`
}

func DefaultAutomation() Automation {
	return Automation{PeriodicSyncEnabled: true, SyncIntervalMinutes: 5, DiscoveryIntervalMinutes: 60}
}

func (a Automation) Validate() error {
	if a.SyncIntervalMinutes < 1 || a.SyncIntervalMinutes > 10_080 {
		return errors.New("sync interval must be between 1 minute and 7 days")
	}
	if a.DiscoveryIntervalMinutes < 5 || a.DiscoveryIntervalMinutes > 43_200 {
		return errors.New("game search interval must be between 5 minutes and 30 days")
	}
	return nil
}

type R2 struct {
	AccountID      string     `json:"accountId"`
	Bucket         string     `json:"bucket"`
	Prefix         string     `json:"prefix"`
	AccessKeyID    string     `json:"accessKeyId"`
	CredentialID   string     `json:"credentialId"`
	LastVerifiedAt *time.Time `json:"lastVerifiedAt,omitempty"`
	LastFailureAt  *time.Time `json:"lastFailureAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
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
	CatalogDB  string
	CatalogTag string
	Blobs      string
}

func DataPaths(root string) Paths {
	return Paths{
		Root:       root,
		Config:     filepath.Join(root, "config.json"),
		Database:   filepath.Join(root, "saveknot.db"),
		Catalog:    filepath.Join(root, "manifest.yaml"),
		CatalogDB:  filepath.Join(root, "catalog.db"),
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
		return Config{Listen: "127.0.0.1:32147", ManifestURL: DefaultManifestURL, RetentionKeep: 50, Automation: DefaultAutomation(), R2: R2{Prefix: "saveknot"}}, nil
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
	// Configurations written before automation was configurable have no interval
	// fields. Preserve the existing five-minute retry behavior when migrating.
	if cfg.Automation.SyncIntervalMinutes == 0 && cfg.Automation.DiscoveryIntervalMinutes == 0 {
		cfg.Automation = DefaultAutomation()
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
