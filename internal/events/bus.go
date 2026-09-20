package events

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

type Bus struct {
	mu          sync.RWMutex
	subscribers map[chan core.Event]struct{}
	store       Store
}

type Store interface {
	SaveEvent(context.Context, core.Event) error
	LoadEventPage(context.Context, time.Time, int, int) ([]core.Event, int, error)
	PruneEvents(context.Context, time.Time, []string) error
}

var transientTypes = []string{
	"catalog.updated",
	"restore.started",
	"snapshot.skipped",
	"snapshot.started",
	"storage.reconcile.started",
	"sync.progress",
	"sync.started",
}

func New() *Bus {
	return &Bus{subscribers: make(map[chan core.Event]struct{})}
}

func NewPersistent(ctx context.Context, store Store) (*Bus, error) {
	if err := store.PruneEvents(ctx, time.Now().UTC(), transientTypes); err != nil {
		return nil, err
	}
	bus := New()
	bus.store = store
	return bus, nil
}

func (b *Bus) Publish(event core.Event) {
	b.PublishContext(context.Background(), event)
}

func (b *Bus) PublishContext(ctx context.Context, event core.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	if b.store != nil && !slices.Contains(transientTypes, event.Type) {
		if err := b.store.SaveEvent(ctx, event); err != nil {
			slog.Error("persist activity event", "type", event.Type, "error", err)
		}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func (b *Bus) Subscribe(size int) (<-chan core.Event, func()) {
	channel := make(chan core.Event, size)
	b.mu.Lock()
	b.subscribers[channel] = struct{}{}
	b.mu.Unlock()
	return channel, func() {
		b.mu.Lock()
		if _, ok := b.subscribers[channel]; ok {
			delete(b.subscribers, channel)
			close(channel)
		}
		b.mu.Unlock()
	}
}

// ActivityPage reads one bounded page directly from durable storage.
func (b *Bus) ActivityPage(ctx context.Context, until time.Time, offset, limit int) ([]core.Event, int, error) {
	if b.store == nil {
		return nil, 0, nil
	}
	return b.store.LoadEventPage(ctx, until, offset, limit)
}

func (b *Bus) Prune(ctx context.Context, now time.Time) error {
	if b.store == nil {
		return nil
	}
	return b.store.PruneEvents(ctx, now, transientTypes)
}
