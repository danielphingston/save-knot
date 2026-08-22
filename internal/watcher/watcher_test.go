package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNearestExistingDirectoryForFutureSavePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	existing := filepath.Join(root, "Game")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	path, err := nearestExistingDirectory(filepath.Join(existing, "Saves", "slot.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if path != existing {
		t.Fatalf("expected %q, got %q", existing, path)
	}
	if !within(existing, filepath.Join(existing, "Saves", "slot.dat")) || within(existing, filepath.Join(root, "Other", "slot.dat")) {
		t.Fatal("path containment check returned the wrong result")
	}
	filesystemRoot := filepath.VolumeName(root) + string(filepath.Separator)
	if _, err := nearestExistingDirectory(filesystemRoot); err == nil {
		t.Fatal("filesystem root was accepted as a watch target")
	}
}

func TestEarlierCapsContinuousWrites(t *testing.T) {
	t.Parallel()
	now := time.Now()
	if got := earlier(now.Add(time.Minute), now.Add(time.Second)); !got.Equal(now.Add(time.Second)) {
		t.Fatalf("unexpected earlier time: %v", got)
	}
}
