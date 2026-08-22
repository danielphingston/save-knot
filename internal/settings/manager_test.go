package settings

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/saveknot/saveknot/internal/config"
)

type memoryVault struct{ values map[string]string }

func (v *memoryVault) Set(id, secret string) error { v.values[id] = secret; return nil }
func (v *memoryVault) Get(id string) (string, error) {
	secret, ok := v.values[id]
	if !ok {
		return "", errors.New("secret not found")
	}
	return secret, nil
}

func TestManagerCreatesStableDeviceIdentity(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	manager, err := New(path, config.Config{Listen: "127.0.0.1:32147"}, &memoryVault{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	deviceID := manager.Config().DeviceID
	if deviceID == "" {
		t.Fatal("device ID was not generated")
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != deviceID {
		t.Fatalf("device identity was not persisted: %q != %q", loaded.DeviceID, deviceID)
	}
	if _, err := manager.R2(context.Background()); !errors.Is(err, ErrR2NotConfigured) {
		t.Fatalf("expected ErrR2NotConfigured, got %v", err)
	}
}
