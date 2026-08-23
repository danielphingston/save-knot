package settings

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/platform"
	"github.com/saveknot/saveknot/internal/remote"
)

var ErrR2NotConfigured = errors.New("R2 is not configured")

type vault interface {
	Set(string, string) error
	Get(string) (string, error)
}

func (m *Manager) ConfigureAutostart(enabled bool) error {
	if err := platform.ConfigureAutostart(enabled); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	updated := m.config
	updated.LaunchAtLogin = enabled
	if err := config.Save(m.path, updated); err != nil {
		return fmt.Errorf("save launch-at-login setting: %w", err)
	}
	m.config = updated
	return nil
}

func (m *Manager) ConfigureRetention(keep int) error {
	if keep < 1 || keep > 10_000 {
		return errors.New("snapshot retention must be between 1 and 10000")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	updated := m.config
	updated.RetentionKeep = keep
	if err := config.Save(m.path, updated); err != nil {
		return fmt.Errorf("save retention setting: %w", err)
	}
	m.config = updated
	return nil
}

func (m *Manager) ConfigureLocal(local config.Local) error {
	local.LocalBackupDir = filepath.Clean(strings.TrimSpace(local.LocalBackupDir))
	if !filepath.IsAbs(local.LocalBackupDir) {
		return errors.New("local backup location must be an absolute path")
	}
	cleanRoots := func(label string, roots []string) ([]string, error) {
		cleaned := make([]string, 0, len(roots))
		for _, root := range roots {
			root = filepath.Clean(strings.TrimSpace(root))
			if root == "." {
				continue
			}
			if !filepath.IsAbs(root) {
				return nil, fmt.Errorf("%s path %q must be absolute", label, root)
			}
			cleaned = append(cleaned, root)
		}
		return cleaned, nil
	}
	steamRoots, err := cleanRoots("Steam root", local.SteamRoots)
	if err != nil {
		return err
	}
	epicManifests, err := cleanRoots("Epic manifest", local.EpicManifests)
	if err != nil {
		return err
	}
	gogRoots, err := cleanRoots("GOG root", local.GOGRoots)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	updated := m.config
	updated.LocalBackupDir = local.LocalBackupDir
	updated.SteamRoots = steamRoots
	updated.EpicManifests = epicManifests
	updated.GOGRoots = gogRoots
	if err := config.Save(m.path, updated); err != nil {
		return fmt.Errorf("save local settings: %w", err)
	}
	m.config = updated
	return nil
}

type Manager struct {
	mu     sync.RWMutex
	path   string
	config config.Config
	vault  vault
}

func New(path string, cfg config.Config, credentialVault vault) (*Manager, error) {
	if cfg.DeviceID == "" {
		id, err := core.NewID(time.Now().UTC())
		if err != nil {
			return nil, err
		}
		cfg.DeviceID = id
		if err := config.Save(path, cfg); err != nil {
			return nil, err
		}
	}
	return &Manager{path: path, config: cfg, vault: credentialVault}, nil
}

func (m *Manager) Config() config.Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.config
}

func (m *Manager) R2(ctx context.Context) (*remote.R2, error) {
	m.mu.RLock()
	settings := m.config.R2
	m.mu.RUnlock()
	if settings.CredentialID == "" {
		return nil, ErrR2NotConfigured
	}
	secret, err := m.vault.Get(settings.CredentialID)
	if err != nil {
		return nil, err
	}
	return remote.NewR2(ctx, settings, secret)
}

func (m *Manager) ConfigureR2(ctx context.Context, settings config.R2, secret string) error {
	settings.Prefix = settings.ObjectPrefix()
	if err := settings.Validate(); err != nil {
		return err
	}
	client, err := remote.NewR2(ctx, settings, secret)
	if err != nil {
		return err
	}
	if err := client.Test(ctx); err != nil {
		return err
	}
	credentialID := "r2-default"
	if err := m.vault.Set(credentialID, secret); err != nil {
		return err
	}
	settings.CredentialID = credentialID
	m.mu.Lock()
	updated := m.config
	updated.R2 = settings
	if err := config.Save(m.path, updated); err != nil {
		m.mu.Unlock()
		return fmt.Errorf("save R2 settings: %w", err)
	}
	m.config = updated
	m.mu.Unlock()
	return nil
}
