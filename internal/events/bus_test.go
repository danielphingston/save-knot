package events

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/database"
)

func TestBusPublishesAndUnsubscribes(t *testing.T) {
	t.Parallel()
	bus := New()
	events, unsubscribe := bus.Subscribe(1)
	bus.Publish(core.Event{Type: "snapshot.completed"})
	select {
	case event := <-events:
		if event.Type != "snapshot.completed" || event.Timestamp.IsZero() {
			t.Fatalf("unexpected event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not published")
	}
	unsubscribe()
	if _, open := <-events; open {
		t.Fatal("subscription channel remained open")
	}
}

func TestPersistentBusReplaysAfterRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	bus, err := NewPersistent(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	bus.Publish(core.Event{Type: "snapshot.completed", GameID: "game-a"})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = database.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	bus, err = NewPersistent(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	stream, unsubscribe := bus.Subscribe(1)
	defer unsubscribe()
	select {
	case event := <-stream:
		if event.Type != "snapshot.completed" || event.GameID != "game-a" {
			t.Fatalf("wrong event replayed: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("persisted activity was not replayed")
	}
	if err := bus.Prune(ctx, time.Now().Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	remaining, err := store.LoadEvents(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("periodic retention did not remove old activity: %#v", remaining)
	}
}

func TestHistoryPageRemainsStableAfterLiveEvents(t *testing.T) {
	t.Parallel()
	bus := New()
	now := time.Now().UTC().Add(-time.Minute)
	for index := range 120 {
		bus.Publish(core.Event{Type: "snapshot.completed", Timestamp: now.Add(time.Duration(index) * time.Millisecond)})
	}
	anchor := now.Add(time.Second)
	first, total := bus.HistoryPage(anchor, 0, 50)
	if total != 120 || len(first) != 50 {
		t.Fatalf("first page: %d of %d", len(first), total)
	}
	bus.Publish(core.Event{Type: "sync.completed", Timestamp: time.Now().UTC()})
	next, total := bus.HistoryPage(anchor, 50, 50)
	if total != 120 || len(next) != 50 || !next[0].Timestamp.Before(first[49].Timestamp) {
		t.Fatal("live event shifted anchored pages")
	}
	last, _ := bus.HistoryPage(anchor, 100, 50)
	if len(last) != 20 || !last[19].Timestamp.Equal(now) {
		t.Fatal("oldest retained event is unreachable")
	}
}

func TestHistoryPageMatchingFiltersBeforePagination(t *testing.T) {
	t.Parallel()
	bus := New()
	now := time.Now().UTC().Add(-time.Minute)
	for index := range 80 {
		typeName := "sync.progress"
		if index%4 == 0 {
			typeName = "snapshot.completed"
		}
		bus.Publish(core.Event{Type: typeName, Timestamp: now.Add(time.Duration(index) * time.Millisecond)})
	}
	entries, total := bus.HistoryPageMatching(now.Add(time.Second), 0, 50, func(event core.Event) bool {
		return event.Type == "snapshot.completed"
	})
	if total != 20 || len(entries) != 20 {
		t.Fatalf("filtered page: %d of %d", len(entries), total)
	}
}

func TestLiveSubscriptionDoesNotAllocateOrReplayHistory(t *testing.T) {
	t.Parallel()
	bus := New()
	for range 1000 {
		bus.Publish(core.Event{Type: "old"})
	}
	channel, cancel := bus.SubscribeLive(16)
	defer cancel()
	if cap(channel) != 16 {
		t.Fatalf("live buffer grew with history: %d", cap(channel))
	}
	select {
	case <-channel:
		t.Fatal("live stream replayed history")
	default:
	}
	bus.Publish(core.Event{Type: "new"})
	select {
	case event := <-channel:
		if event.Type != "new" {
			t.Fatal("wrong live event")
		}
	default:
		t.Fatal("live event not delivered")
	}
}
