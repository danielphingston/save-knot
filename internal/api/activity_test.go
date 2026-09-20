package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/database"
	"github.com/saveknot/saveknot/internal/events"
)

func TestActivityPagesAreBoundedAndValidateCursors(t *testing.T) {
	t.Parallel()
	store, err := database.Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	bus, err := events.NewPersistent(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(-time.Minute)
	for index := range 125 {
		bus.Publish(core.Event{Type: "snapshot.completed", Timestamp: now.Add(time.Duration(index) * time.Millisecond)})
	}
	bus.Publish(core.Event{Type: "sync.progress", Timestamp: now.Add(time.Second)})
	server := &Server{events: bus}
	response := httptest.NewRecorder()
	server.activity(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/activity", nil))
	var page struct {
		Events []core.Event `json:"events"`
		Total  int          `json:"total"`
		Until  string       `json:"until"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || len(page.Events) != 50 || page.Total != 125 || page.Until == "" {
		t.Fatalf("unexpected activity page: %s", response.Body.String())
	}
	if !page.Events[0].Timestamp.After(page.Events[1].Timestamp) {
		t.Fatal("history must be newest first")
	}
	for _, event := range page.Events {
		if event.Type == "sync.progress" {
			t.Fatal("internal progress event leaked into user activity")
		}
	}
	for _, query := range []string{"offset=-1", "offset=nope", "until=bad", "until=9999-01-01T00:00:00Z"} {
		response := httptest.NewRecorder()
		server.activity(response, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/activity?"+query, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("accepted invalid query %s", query)
		}
	}
}

func TestPlainUIAssetsAreEmbedded(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"index.html", "styles.css", "app.js", "helpers.mjs"} {
		data, err := webFiles.ReadFile("web/" + name)
		if err != nil || len(data) == 0 {
			t.Fatalf("missing UI asset %s: %v", name, err)
		}
		if strings.Contains(string(data), "solid-js") {
			t.Fatalf("framework code remains in %s", name)
		}
	}
}

type streamResponse struct {
	*httptest.ResponseRecorder
	deadline time.Time
	changed  bool
}

func (writer *streamResponse) SetWriteDeadline(deadline time.Time) error {
	writer.deadline = deadline
	writer.changed = true
	return nil
}

func TestLiveStreamClearsResponseDeadlineAndFlushesHeaders(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/events?live=true", nil)
	response := &streamResponse{ResponseRecorder: httptest.NewRecorder()}
	server := &Server{events: events.New()}
	server.eventStream(response, request)
	if !response.changed || !response.deadline.IsZero() {
		t.Fatal("stream still has a finite response deadline")
	}
	if !response.Flushed || !strings.Contains(response.Body.String(), ": connected") {
		t.Fatal("idle stream did not flush its initial headers")
	}
}
