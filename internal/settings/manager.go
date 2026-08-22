package settings

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/remote"
)

var ErrR2NotConfigured = errors.New("R2 is not configured")

type vault interface {
	Set(string, string) error
	Get(string) (string, error)
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
