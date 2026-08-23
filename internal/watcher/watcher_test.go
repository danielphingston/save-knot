package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
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

func TestSnapshotDueDebouncesAndEnforcesMinimumGap(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	policy := core.DefaultBackupPolicy()
	if got := snapshotDue(now, now, time.Time{}, policy); !got.Equal(now.Add(time.Duration(policy.QuietSeconds) * time.Second)) {
		t.Fatalf("quiet debounce not applied: %v", got)
	}
	last := now.Add(-time.Minute)
	if got := snapshotDue(now, now, last, policy); !got.Equal(last.Add(time.Duration(policy.MinGapSeconds) * time.Second)) {
		t.Fatalf("minimum gap not applied: %v", got)
	}
	maximumDelay := time.Duration(policy.MaxDirtySeconds) * time.Second
	first := now.Add(-maximumDelay)
	if got := snapshotDue(now, first, last, policy); !got.Equal(first.Add(maximumDelay)) {
		t.Fatalf("maximum dirty duration not applied: %v", got)
	}
}

func TestPrunePendingDropsGamesRemovedFromWatchTargets(t *testing.T) {
	t.Parallel()
	pending := map[string]pendingChange{"active": {}, "removed": {}}
	prunePending([]Target{{GameID: "active", Path: "/saves"}}, pending)
	if _, present := pending["removed"]; present {
		t.Fatal("removed game retained a queued backup")
	}
	if _, present := pending["active"]; !present {
		t.Fatal("active game lost its queued backup")
	}
}
