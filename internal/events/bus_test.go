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

func TestPersistentBusLoadsDurableActivityOnDemand(t *testing.T) {
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
	live, unsubscribe := bus.Subscribe(2)
	bus.Publish(core.Event{Type: "sync.progress", GameID: "game-a"})
	bus.Publish(core.Event{Type: "snapshot.completed", GameID: "game-a"})
	unsubscribe()
	for _, eventType := range []string{"sync.progress", "snapshot.completed"} {
		event := <-live
		if event.Type != eventType {
			t.Fatalf("live event = %q, want %q", event.Type, eventType)
		}
	}
	// Simulate a transient row written by an older build. Reopening the bus
	// should remove it without loading activity history into memory.
	if err := store.SaveEvent(ctx, core.Event{Type: "sync.started", Timestamp: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
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
	select {
	case event := <-stream:
		t.Fatalf("persisted activity was replayed into the live stream: %#v", event)
	default:
	}
	unsubscribe()
	activity, total, err := bus.ActivityPage(ctx, time.Now().Add(time.Second), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(activity) != 1 || activity[0].Type != "snapshot.completed" || activity[0].GameID != "game-a" {
		t.Fatalf("unexpected durable activity: total=%d events=%#v", total, activity)
	}
	if err := bus.Prune(ctx, time.Now().Add(31*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	remaining, total, err := bus.ActivityPage(ctx, time.Now().Add(31*24*time.Hour), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 0 || len(remaining) != 0 {
		t.Fatalf("periodic retention did not remove old activity: %#v", remaining)
	}
}

func TestActivityPagesRemainStableAfterLiveEvents(t *testing.T) {
	ctx := context.Background()
	store, err := database.Open(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	bus, err := NewPersistent(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	for index := range 120 {
		bus.Publish(core.Event{Type: "snapshot.completed", Timestamp: now.Add(time.Duration(index) * time.Millisecond)})
	}
	anchor := now.Add(time.Second)
	first, total, err := bus.ActivityPage(ctx, anchor, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 120 || len(first) != 50 {
		t.Fatalf("first page: %d of %d", len(first), total)
	}
	bus.Publish(core.Event{Type: "sync.completed", Timestamp: time.Now().UTC()})
	next, total, err := bus.ActivityPage(ctx, anchor, 50, 50)
	if err != nil {
		t.Fatal(err)
	}
	if total != 120 || len(next) != 50 || !next[0].Timestamp.Before(first[49].Timestamp) {
		t.Fatal("live event shifted anchored pages")
	}
	last, _, err := bus.ActivityPage(ctx, anchor, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(last) != 20 || !last[19].Timestamp.Equal(now) {
		t.Fatal("oldest retained event is unreachable")
	}
}

func TestSubscriptionDoesNotReplayEarlierEvents(t *testing.T) {
	t.Parallel()
	bus := New()
	for range 1000 {
		bus.Publish(core.Event{Type: "old"})
	}
	channel, cancel := bus.Subscribe(16)
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
