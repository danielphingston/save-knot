package events

import (
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
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
