package core

import "time"

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
	LastSeen      *time.Time `json:"lastSeen,omitempty"`
	LastChange    *time.Time `json:"lastChange,omitempty"`
	LastBackup    *time.Time `json:"lastBackup,omitempty"`
	SnapshotCount int        `json:"snapshotCount"`
	StoredSize    int64      `json:"storedSize"`
}

type GamePath struct {
	ID       string `json:"id"`
	GameID   string `json:"gameId"`
	Source   string `json:"source"`
	Template string `json:"template"`
	Resolved string `json:"resolved"`
	Enabled  bool   `json:"enabled"`
}

type Snapshot struct {
	Version      int            `json:"version"`
	ID           string         `json:"id"`
	GameID       string         `json:"gameId"`
	GameName     string         `json:"gameName"`
	DeviceID     string         `json:"deviceId"`
	CreatedAt    time.Time      `json:"createdAt"`
	Files        []SnapshotFile `json:"files"`
	OriginalSize int64          `json:"originalSize"`
	StoredSize   int64          `json:"storedSize"`
	RemoteState  string         `json:"remoteState"`
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
