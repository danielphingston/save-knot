package events

import (
	"sync"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

type Bus struct {
	mu          sync.RWMutex
	subscribers map[chan core.Event]struct{}
	history     []core.Event
}

func New() *Bus {
	return &Bus{subscribers: make(map[chan core.Event]struct{})}
}

func (b *Bus) Publish(event core.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.history = append(b.history, event)
	if len(b.history) > 200 {
		b.history = append([]core.Event(nil), b.history[len(b.history)-200:]...)
	}
	for subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

func (b *Bus) Subscribe(size int) (<-chan core.Event, func()) {
	b.mu.Lock()
	if size < len(b.history) {
		size = len(b.history)
	}
	channel := make(chan core.Event, size)
	for _, event := range b.history {
		channel <- event
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
