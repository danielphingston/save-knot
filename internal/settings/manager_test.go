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
func (v *memoryVault) Delete(id string) error { delete(v.values, id); return nil }

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

func TestRetentionPersistsWithoutChangingOtherSettings(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	cfg := config.Config{Listen: "127.0.0.1:32147", DeviceID: "device-a", RetentionKeep: 50, LocalBackupDir: "/backups"}
	manager, err := New(path, cfg, &memoryVault{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.ConfigureRetention(51); err != nil {
		t.Fatal(err)
	}
	if manager.Config().RetentionKeep != 51 {
		t.Fatalf("in-memory retention was not updated: %#v", manager.Config())
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RetentionKeep != 51 || loaded.LocalBackupDir != "/backups" || loaded.DeviceID != "device-a" {
		t.Fatalf("retention update corrupted config: %#v", loaded)
	}
}

func TestAutomationPersistsIndependentSchedules(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	manager, err := New(path, config.Config{Listen: "127.0.0.1:32147", DeviceID: "device-a"}, &memoryVault{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	automation := config.Automation{SyncIntervalMinutes: 30, PeriodicDiscoveryEnabled: true, DiscoveryIntervalMinutes: 180}
	if err := manager.ConfigureAutomation(automation); err != nil {
		t.Fatal(err)
	}
	if manager.Config().Automation != automation {
		t.Fatalf("automation was not stored: %#v", manager.Config().Automation)
	}
	if err := manager.ConfigureAutomation(config.Automation{}); err == nil {
		t.Fatal("invalid zero intervals were accepted")
	}
}

func TestDisconnectR2ClearsCredentialAndConfiguration(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	vault := &memoryVault{values: map[string]string{"r2-default": "secret"}}
	cfg := config.Config{Listen: "127.0.0.1:32147", DeviceID: "device-a", RetentionKeep: 51, R2: config.R2{AccountID: "account", Bucket: "bucket", AccessKeyID: "key", CredentialID: "r2-default", Prefix: "prefix"}}
	manager, err := New(path, cfg, vault)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.DisconnectR2(); err != nil {
		t.Fatal(err)
	}
	if _, present := vault.values["r2-default"]; present {
		t.Fatal("R2 credential remained in the vault")
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.R2.CredentialID != "" || loaded.R2.AccountID != "" || loaded.RetentionKeep != 51 {
		t.Fatalf("disconnect left active R2 metadata or changed unrelated settings: %#v", loaded)
	}
	if err := manager.DisconnectR2(); err != nil {
		t.Fatalf("disconnect was not idempotent: %v", err)
	}
}

func TestR2HealthTracksVerificationAndFailure(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	manager, err := New(path, config.Config{
		Listen: "127.0.0.1:32147", DeviceID: "device-a",
		R2: config.R2{Bucket: "bucket", CredentialID: "r2-default"},
	}, &memoryVault{values: map[string]string{"r2-default": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	manager.RecordR2Failure(errors.New("bucket unavailable"))
	failed := manager.Config().R2
	if failed.LastFailureAt == nil || failed.LastError != "bucket unavailable" || failed.LastVerifiedAt != nil {
		t.Fatalf("R2 failure was not recorded: %#v", failed)
	}
	manager.RecordR2Success()
	verified := manager.Config().R2
	if verified.LastVerifiedAt == nil || verified.LastFailureAt != nil || verified.LastError != "" {
		t.Fatalf("R2 verification did not clear degraded state: %#v", verified)
	}
	if verified.LastSyncedAt != nil {
		t.Fatalf("R2 verification was incorrectly recorded as a sync: %#v", verified)
	}
	manager.RecordR2Sync()
	synced := manager.Config().R2
	if synced.LastSyncedAt == nil || synced.LastVerifiedAt == nil {
		t.Fatalf("R2 sync time was not recorded: %#v", synced)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.R2.LastSyncedAt == nil {
		t.Fatalf("R2 sync time was not persisted: %#v", loaded.R2)
	}
}
