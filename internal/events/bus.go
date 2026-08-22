package events

import (
	"sync"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

type Bus struct {
	mu          sync.RWMutex
	subscribers map[chan core.Event]struct{}
}

func New() *Bus {
	return &Bus{subscribers: make(map[chan core.Event]struct{})}
}

func (b *Bus) Publish(event core.Event) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
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
