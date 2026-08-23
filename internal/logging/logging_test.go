package logging

import (
	"log/slog"
	"os"
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
