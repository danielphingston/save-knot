//go:build !linux && !darwin && !windows

package database

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRenameNoReplaceUnsupportedPlatformFailsClosed(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	sourceData := []byte("source")
	destinationData := []byte("preexisting destination")
	if err := os.WriteFile(source, sourceData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, destinationData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := renameNoReplace(source, destination); !errors.Is(err, errNoReplaceRenameUnsupported) {
		t.Fatalf("rename error = %v; want unsupported no-replace error", err)
	}
	gotSource, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(gotSource, sourceData) {
		t.Fatalf("source changed: data=%q, err=%v", gotSource, err)
	}
	gotDestination, err := os.ReadFile(destination)
	if err != nil || !bytes.Equal(gotDestination, destinationData) {
		t.Fatalf("destination changed: data=%q, err=%v", gotDestination, err)
	}
}
