package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFlushPendingRetainsDueGameWhenJobQueueIsFull(t *testing.T) {
	t.Parallel()
	m := &Manager{jobs: make(chan string, 1)}
	m.jobs <- "busy"
	now := time.Now()
	pending := map[string]pendingChange{"waiting": {first: now.Add(-time.Second), due: now}}
	lastQueued := make(map[string]time.Time)
	m.flushPending(now, pending, lastQueued)
	if _, ok := pending["waiting"]; !ok {
		t.Fatal("full queue dropped the pending backup")
	}
	if !lastQueued["waiting"].IsZero() {
		t.Fatal("failed enqueue incorrectly advanced the minimum-gap clock")
	}
	<-m.jobs
	m.flushPending(now.Add(time.Second), pending, lastQueued)
	if got := <-m.jobs; got != "waiting" {
		t.Fatalf("retry queued %q, want waiting", got)
	}
	if _, ok := pending["waiting"]; ok {
		t.Fatal("successful enqueue retained stale pending work")
	}
}

func TestApplyTargetsRemovesObsoleteDirectoryWatches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for _, path := range []string{first, second} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	m, err := New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close watcher: %v", err)
		}
	})
	m.applyTargets([]Target{{GameID: "first", Path: first}})
	m.applyTargets([]Target{{GameID: "second", Path: second}})
	watched := make(map[string]bool)
	for _, path := range m.watcher.WatchList() {
		watched[path] = true
	}
	if watched[first] {
		t.Fatalf("removed target %q still has an OS watch", first)
	}
	if !watched[second] {
		t.Fatalf("active target %q has no OS watch", second)
	}
}
