package events

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

const memoryProbeSize = 4 << 20

type memoryProbe struct {
	data []byte
}

type onDemandStore struct {
	loadCalls  int
	pruneCalls int
	saved      []core.Event
	page       []core.Event
	total      int
}

func (store *onDemandStore) SaveEvent(_ context.Context, event core.Event) error {
	store.saved = append(store.saved, event)
	return nil
}

func (store *onDemandStore) LoadEventPage(_ context.Context, _ time.Time, _, _ int) ([]core.Event, int, error) {
	store.loadCalls++
	return store.page, store.total, nil
}

func (store *onDemandStore) PruneEvents(_ context.Context, _ time.Time, _ []string) error {
	store.pruneCalls++
	return nil
}

func TestPersistentBusLoadsHistoryOnlyWhenRequested(t *testing.T) {
	ctx := context.Background()
	store := &onDemandStore{
		page:  []core.Event{{Type: "snapshot.completed"}},
		total: 1,
	}
	bus, err := NewPersistent(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if store.pruneCalls != 1 || store.loadCalls != 0 {
		t.Fatalf("startup prunes without loading history: prunes=%d loads=%d", store.pruneCalls, store.loadCalls)
	}

	stream, unsubscribe := bus.Subscribe(1)
	unsubscribe()
	if _, open := <-stream; open {
		t.Fatal("unsubscribed stream remained open")
	}
	bus.Publish(core.Event{Type: "sync.progress"})
	bus.Publish(core.Event{Type: "snapshot.completed"})
	if store.loadCalls != 0 {
		t.Fatalf("live activity loaded durable history %d times", store.loadCalls)
	}
	if len(store.saved) != 1 || store.saved[0].Type != "snapshot.completed" {
		t.Fatalf("unexpected persisted events: %#v", store.saved)
	}

	page, total, err := bus.ActivityPage(ctx, time.Now().UTC(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if store.loadCalls != 1 || total != 1 || len(page) != 1 {
		t.Fatalf("on-demand page was not loaded exactly once: loads=%d total=%d page=%d", store.loadCalls, total, len(page))
	}
}

func TestRepeatedSubscriptionsAreReleased(t *testing.T) {
	bus := New()
	for range 10_000 {
		stream, unsubscribe := bus.Subscribe(1)
		unsubscribe()
		unsubscribe()
		if _, open := <-stream; open {
			t.Fatal("unsubscribed stream remained open")
		}
	}

	bus.mu.RLock()
	remaining := len(bus.subscribers)
	bus.mu.RUnlock()
	if remaining != 0 {
		t.Fatalf("subscriber registry retained %d closed streams", remaining)
	}
}

func TestPublishedPayloadIsCollectable(t *testing.T) {
	bus := New()
	finalized := make(chan struct{}, 1)
	publishMemoryProbe(bus, finalized)

	collected := waitForCollection(finalized)
	runtime.KeepAlive(bus)
	if !collected {
		t.Fatal("event bus retained a published payload after delivery")
	}
}

// Keeping this operation out of the test's stack makes the payload unreachable
// immediately after Publish returns unless the bus stores it.
//
//go:noinline
func publishMemoryProbe(bus *Bus, finalized chan<- struct{}) {
	probe := &memoryProbe{data: make([]byte, memoryProbeSize)}
	probe.data[0] = 1
	probe.data[len(probe.data)-1] = 1
	runtime.SetFinalizer(probe, func(*memoryProbe) {
		finalized <- struct{}{}
	})
	bus.Publish(core.Event{Type: "sync.progress", Data: map[string]any{"probe": probe}})
}

func waitForCollection(finalized <-chan struct{}) bool {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runtime.GC()
		select {
		case <-finalized:
			return true
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	return false
}
