package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsAndRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != "127.0.0.1:32147" || cfg.ManifestURL != DefaultManifestURL || cfg.R2.Prefix != "saveknot" {
		t.Fatalf("unexpected defaults: %#v", cfg)
	}
	cfg.DeviceID = "device-a"
	cfg.R2.Bucket = "saves"
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DeviceID != cfg.DeviceID || loaded.R2.Bucket != cfg.R2.Bucket {
		t.Fatalf("round trip mismatch: %#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config is accessible outside the user: %o", info.Mode().Perm())
	}
}

func TestR2Validation(t *testing.T) {
	t.Parallel()
	valid := R2{AccountID: "account", Bucket: "bucket", AccessKeyID: "key", Prefix: "/saveknot/"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := valid.ObjectPrefix(); got != "saveknot" {
		t.Fatalf("unexpected prefix: %q", got)
	}
	invalid := valid
	invalid.Bucket = "bucket/path"
	if err := invalid.Validate(); err == nil {
		t.Fatal("expected bucket path to be rejected")
	}
}
