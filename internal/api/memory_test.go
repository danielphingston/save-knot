package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/events"
)

type streamMemoryProbe struct {
	data []byte
}

func TestRepeatedEventStreamDisconnectsReleaseSubscribers(t *testing.T) {
	bus := events.New()
	server := &Server{events: bus}
	for range 1_000 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/events?live=true", nil)
		response := &streamResponse{ResponseRecorder: httptest.NewRecorder()}
		server.eventStream(response, request)
		if !response.Flushed {
			t.Fatal("event stream did not initialize before disconnecting")
		}
	}

	finalized := make(chan struct{}, 1)
	publishStreamMemoryProbe(bus, finalized)
	collected := waitForStreamProbe(finalized)
	runtime.KeepAlive(bus)
	if !collected {
		t.Fatal("closed event streams retained a subsequently published payload")
	}
}

//go:noinline
func publishStreamMemoryProbe(bus *events.Bus, finalized chan<- struct{}) {
	probe := &streamMemoryProbe{data: make([]byte, 4<<20)}
	probe.data[0] = 1
	probe.data[len(probe.data)-1] = 1
	runtime.SetFinalizer(probe, func(*streamMemoryProbe) {
		finalized <- struct{}{}
	})
	bus.Publish(core.Event{Type: "sync.progress", Data: map[string]any{"probe": probe}})
}

func waitForStreamProbe(finalized <-chan struct{}) bool {
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
