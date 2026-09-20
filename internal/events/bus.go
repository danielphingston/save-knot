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
	history     []core.Event
	store       Store
}

type Store interface {
	SaveEvent(context.Context, core.Event) error
	LoadEvents(context.Context, time.Time) ([]core.Event, error)
	PruneEvents(context.Context, time.Time) error
}

func New() *Bus {
	return &Bus{subscribers: make(map[chan core.Event]struct{})}
}

func NewPersistent(ctx context.Context, store Store) (*Bus, error) {
	history, err := store.LoadEvents(ctx, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	bus := New()
	bus.store = store
	bus.history = history
	return bus, nil
}

func (b *Bus) Publish(event core.Event) {
	b.PublishContext(context.Background(), event)
}

func (b *Bus) PublishContext(ctx context.Context, event core.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.store != nil {
		if err := b.store.SaveEvent(ctx, event); err != nil {
			slog.Error("persist activity event", "type", event.Type, "error", err)
		}
	}
	b.history = append(b.history, event)
	b.pruneHistory()
	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func (b *Bus) Subscribe(size int) (<-chan core.Event, func()) {
	return b.subscribe(size, true)
}

// SubscribeLive avoids replaying the entire retained history to a UI connection.
// History is fetched separately in bounded pages.
func (b *Bus) SubscribeLive(size int) (<-chan core.Event, func()) {
	return b.subscribe(size, false)
}

func (b *Bus) subscribe(size int, replay bool) (<-chan core.Event, func()) {
	b.mu.Lock()
	b.pruneHistory()
	if replay && size < len(b.history) {
		size = len(b.history)
	}
	channel := make(chan core.Event, size)
	if replay {
		for _, event := range b.history {
			channel <- event
		}
	}
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

// HistoryPage returns newest entries first without copying the full history.
// The fixed upper bound keeps later live events from shifting page offsets.
func (b *Bus) HistoryPage(until time.Time, offset, limit int) ([]core.Event, int) {
	return b.HistoryPageMatching(until, offset, limit, nil)
}

// HistoryPageMatching returns a stable newest-first page containing only
// entries accepted by include. A nil predicate includes every event.
func (b *Bus) HistoryPageMatching(until time.Time, offset, limit int, include func(core.Event) bool) ([]core.Event, int) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	entries := make([]core.Event, 0, limit)
	cutoff := time.Now().UTC().Add(-core.ActivityRetention)
	total := 0
	for index := len(b.history) - 1; index >= 0; index-- {
		event := b.history[index]
		if event.Timestamp.After(until) || event.Timestamp.Before(cutoff) || include != nil && !include(event) {
			continue
		}
		if total >= offset && len(entries) < limit {
			entries = append(entries, event)
		}
		total++
	}
	return entries, total
}

func (b *Bus) Prune(ctx context.Context, now time.Time) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.store != nil {
		if err := b.store.PruneEvents(ctx, now); err != nil {
			return err
		}
	}
	cutoff := now.UTC().Add(-core.ActivityRetention)
	b.history = slices.DeleteFunc(b.history, func(event core.Event) bool {
		return event.Timestamp.Before(cutoff)
	})
	return nil
}

// pruneHistory is called while b.mu is locked.
func (b *Bus) pruneHistory() {
	cutoff := time.Now().UTC().Add(-core.ActivityRetention)
	b.history = slices.DeleteFunc(b.history, func(event core.Event) bool { return event.Timestamp.Before(cutoff) })
}
