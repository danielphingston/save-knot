package database

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

func TestActivityEventsSurviveReopenAndExpire(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	event := core.Event{Type: "snapshot.completed", GameID: "game-a", Timestamp: now, Data: map[string]any{"snapshotId": "snapshot-a"}}
	if err := store.SaveEvent(ctx, event); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	events, err := store.LoadEvents(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != event.Type || events[0].GameID != event.GameID || events[0].Data["snapshotId"] != "snapshot-a" {
		t.Fatalf("event did not survive restart: %#v", events)
	}
	events, err = store.LoadEvents(ctx, now.Add(ActivityRetention+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expired events were retained: %#v", events)
	}
}
