package core

import (
	"errors"
	"time"
)

var ErrGameNotFound = errors.New("game not found")

type Game struct {
	ID            string     `json:"id"`
	CatalogID     string     `json:"catalogId,omitempty"`
	CatalogName   string     `json:"catalogName,omitempty"`
	DisplayName   string     `json:"displayName"`
	Store         string     `json:"store"`
	StoreID       string     `json:"storeId,omitempty"`
	InstallPath   string     `json:"installPath,omitempty"`
	Image         string     `json:"image,omitempty"`
	Notes         string     `json:"notes,omitempty"`
	Enabled       bool       `json:"enabled"`
	SyncEnabled   bool       `json:"syncEnabled"`
	Hidden        bool       `json:"hidden,omitempty"`
	LastSeen      *time.Time `json:"lastSeen,omitempty"`
	LastChange    *time.Time `json:"lastChange,omitempty"`
	LastBackup    *time.Time `json:"lastBackup,omitempty"`
	SnapshotCount int        `json:"snapshotCount"`
	StoredSize    int64      `json:"storedSize"`
	PendingCount  int        `json:"pendingSnapshotCount"`
}

type SyncResult struct {
	Eligible int    `json:"eligible"`
	Synced   int    `json:"synced"`
	Failed   int    `json:"failed"`
	Error    string `json:"error,omitempty"`
}

type GamePath struct {
	ID       string `json:"id"`
	GameID   string `json:"gameId"`
	Source   string `json:"source"`
	Template string `json:"template"`
	Resolved string `json:"resolved"`
	Enabled  bool   `json:"enabled"`
}

type RegistryPath struct {
	ID      string `json:"id"`
	GameID  string `json:"gameId"`
	Source  string `json:"source"`
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

type GameExclusion struct {
	ID      string `json:"id"`
	GameID  string `json:"gameId"`
	Pattern string `json:"pattern"`
}

type CatalogChoice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type BackupPolicy struct {
	QuietSeconds    int `json:"quietSeconds"`
	MinGapSeconds   int `json:"minGapSeconds"`
	MaxDirtySeconds int `json:"maxDirtySeconds"`
}

func DefaultBackupPolicy() BackupPolicy {
	return BackupPolicy{QuietSeconds: 10, MinGapSeconds: 300, MaxDirtySeconds: 900}
}

type Snapshot struct {
	Version      int                  `json:"version"`
	ID           string               `json:"id"`
	GameID       string               `json:"gameId"`
	GameName     string               `json:"gameName"`
	DeviceID     string               `json:"deviceId"`
	CreatedAt    time.Time            `json:"createdAt"`
	Files        []SnapshotFile       `json:"files"`
	Registry     *SnapshotRegistry    `json:"registry,omitempty"`
	Metadata     PortableGameMetadata `json:"metadata"`
	OriginalSize int64                `json:"originalSize"`
	StoredSize   int64                `json:"storedSize"`
	RemoteState  string               `json:"remoteState"`
}

type PortableGameMetadata struct {
	DisplayName string `json:"displayName"`
	Notes       string `json:"notes,omitempty"`
	Image       string `json:"image,omitempty"`
}

type SnapshotRegistry struct {
	Keys []string `json:"keys"`
	Hash string   `json:"hash"`
	Size int64    `json:"size"`
}

type SnapshotFile struct {
	SourceKey string    `json:"sourceKey"`
	Path      string    `json:"path"`
	RootFile  bool      `json:"rootFile,omitempty"`
	Hash      string    `json:"hash"`
	Size      int64     `json:"size"`
	Mode      uint32    `json:"mode"`
	Modified  time.Time `json:"modified"`
}

type GameUpdate struct {
	DisplayName *string `json:"displayName"`
	Image       *string `json:"image"`
	Notes       *string `json:"notes"`
	Enabled     *bool   `json:"enabled"`
	SyncEnabled *bool   `json:"syncEnabled"`
}

type ManualGame struct {
	Name  string   `json:"name"`
	Image string   `json:"image"`
	Paths []string `json:"paths"`
}

type Event struct {
	Type      string         `json:"type"`
	GameID    string         `json:"gameId,omitempty"`
	Message   string         `json:"message,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
	Data      map[string]any `json:"data,omitempty"`
}

type Diagnostics struct {
	Catalog   CatalogDiagnostics   `json:"catalog"`
	Discovery DiscoveryDiagnostics `json:"discovery"`
}

type CatalogDiagnostics struct {
	Loaded      bool       `json:"loaded"`
	GameCount   int        `json:"gameCount"`
	CachePath   string     `json:"cachePath"`
	LastChecked *time.Time `json:"lastChecked,omitempty"`
	LastError   string     `json:"lastError,omitempty"`
}

type DiscoveryDiagnostics struct {
	LastRun         *time.Time `json:"lastRun,omitempty"`
	SteamRoots      []string   `json:"steamRoots"`
	SteamInstalled  int        `json:"steamInstalled"`
	EpicInstalled   int        `json:"epicInstalled"`
	GOGInstalled    int        `json:"gogInstalled"`
	CatalogMatched  int        `json:"catalogMatched"`
	GamesRegistered int        `json:"gamesRegistered"`
	LocalSaveGames  int        `json:"localSaveGames"`
	DeepScanMillis  int64      `json:"deepScanMillis"`
	Unmatched       []string   `json:"unmatched"`
	LastError       string     `json:"lastError,omitempty"`
}
