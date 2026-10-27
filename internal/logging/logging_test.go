package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigurePersistsStructuredLogs(t *testing.T) {
	closer, path, err := Configure(t.TempDir(), "debug")
	if err != nil {
		t.Fatal(err)
	}
	slog.Debug("discovery detail", "games", 7)
	if err := closer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents := string(data)
	if !strings.Contains(contents, "discovery detail") || !strings.Contains(contents, "games=7") {
		t.Fatalf("structured log was not persisted: %q", contents)
	}
}

func TestConfigureRejectsInvalidLevel(t *testing.T) {
	if _, _, err := Configure(t.TempDir(), "verbose"); err == nil {
		t.Fatal("invalid log level was accepted")
	}
}

func TestRotateFailureKeepsLogWritable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "saveknot.log")
	file, err := openRotating(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	})
	// A directory at the backup name makes renaming the active log fail.
	if err := os.MkdirAll(filepath.Join(dir, "saveknot.log.1", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	file.size = maxLogSize
	if _, err := file.Write([]byte("first\n")); err != nil {
		t.Fatalf("write after failed rotation: %v", err)
	}
	if _, err := file.Write([]byte("second\n")); err != nil {
		t.Fatalf("second write after failed rotation: %v", err)
	}
}
