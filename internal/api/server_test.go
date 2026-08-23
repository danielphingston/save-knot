package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/events"
	"github.com/saveknot/saveknot/internal/remote"
	"github.com/saveknot/saveknot/internal/settings"
)

type repositoryFake struct {
	games []core.Game
	paths []core.GamePath
}

func (r *repositoryFake) ListGames(context.Context) ([]core.Game, error) { return r.games, nil }
func (r *repositoryFake) Game(_ context.Context, id string) (core.Game, error) {
	for _, game := range r.games {
		if game.ID == id {
			return game, nil
		}
	}
	return core.Game{}, context.Canceled
}

func (r *repositoryFake) GamePaths(context.Context, string) ([]core.GamePath, error) {
	return r.paths, nil
}

func (r *repositoryFake) GameRegistry(context.Context, string) ([]core.RegistryPath, error) {
	return nil, nil
}

func (r *repositoryFake) ListExclusions(context.Context, string) ([]core.GameExclusion, error) {
	return nil, nil
}

func (r *repositoryFake) GamePolicy(context.Context, string) (core.BackupPolicy, error) {
	return core.DefaultBackupPolicy(), nil
}

func (r *repositoryFake) ListSnapshots(context.Context, string) ([]core.Snapshot, error) {
	return nil, nil
}

func (r *repositoryFake) UpsertGame(_ context.Context, game core.Game) error {
	r.games = append(r.games, game)
	return nil
}

func (r *repositoryFake) AddPath(_ context.Context, path core.GamePath) error {
	r.paths = append(r.paths, path)
	return nil
}
func (r *repositoryFake) UpdateGame(context.Context, string, core.GameUpdate) error { return nil }
func (r *repositoryFake) UpdatePath(context.Context, string, string, bool) error    { return nil }
func (r *repositoryFake) DeletePath(context.Context, string, string) error          { return nil }
func (r *repositoryFake) AddExclusion(context.Context, core.GameExclusion) error    { return nil }
func (r *repositoryFake) DeleteExclusion(context.Context, string, string) error     { return nil }
func (r *repositoryFake) UpdateGamePolicy(context.Context, string, core.BackupPolicy) error {
	return nil
}

type coordinatorFake struct{ reconciled int }

func (c *coordinatorFake) Backup(context.Context, string) (core.Snapshot, error) {
	return core.Snapshot{}, nil
}

func (c *coordinatorFake) Restore(context.Context, string, string) (core.Snapshot, error) {
	return core.Snapshot{}, nil
}
func (c *coordinatorFake) ReconcileWatches(context.Context)                           { c.reconciled++ }
func (c *coordinatorFake) SyncPending(context.Context) error                          { return nil }
func (c *coordinatorFake) SyncGame(context.Context, string) error                     { return nil }
func (c *coordinatorFake) DeleteSnapshot(context.Context, string, string, bool) error { return nil }
func (c *coordinatorFake) DiscoverNow(context.Context) core.Diagnostics               { return core.Diagnostics{} }
func (c *coordinatorFake) ConfigureLocal(context.Context, config.Local) error         { return nil }
func (c *coordinatorFake) ReconcileRemote(context.Context) (remote.ReconcileResult, error) {
	return remote.ReconcileResult{}, nil
}
func (c *coordinatorFake) Diagnostics() core.Diagnostics { return core.Diagnostics{} }
func (c *coordinatorFake) SearchCatalog(context.Context, string) ([]core.CatalogChoice, error) {
	return nil, nil
}
func (c *coordinatorFake) RemapGame(context.Context, string, string) error { return nil }

type vaultFake struct{ values map[string]string }

func (v *vaultFake) Set(id, value string) error    { v.values[id] = value; return nil }
func (v *vaultFake) Get(id string) (string, error) { return v.values[id], nil }

func TestServerIsLoopbackOnly(t *testing.T) {
	t.Parallel()
	if err := validateLoopback("127.0.0.1:32147"); err != nil {
		t.Fatal(err)
	}
	if err := validateLoopback("0.0.0.0:32147"); err == nil {
		t.Fatal("public listen address was accepted")
	}
}

func TestStatusAndManualGameAPI(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	manager, err := settings.New(filepath.Join(directory, "config.json"), config.Config{Listen: "127.0.0.1:32147", DeviceID: "device-a"}, &vaultFake{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	repository := &repositoryFake{}
	coordinator := &coordinatorFake{}
	server, err := New("127.0.0.1:32147", repository, repository, repository, repository, coordinator, coordinator, coordinator, manager, events.New(), filepath.Join(directory, "artwork"))
	if err != nil {
		t.Fatal(err)
	}
	statusRequest := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/status", nil)
	statusRequest.Host = "127.0.0.1:32147"
	statusResponse := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || statusResponse.Header().Get("Content-Security-Policy") == "" {
		t.Fatalf("unexpected status response: %d, headers=%v", statusResponse.Code, statusResponse.Header())
	}
	var status map[string]any
	if err := json.Unmarshal(statusResponse.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["deviceId"] != "device-a" || status["r2Configured"] != false {
		t.Fatalf("unexpected status: %#v", status)
	}

	body := `{"name":"Example","paths":["/tmp/example-saves"]}`
	addRequest := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/games", strings.NewReader(body))
	addRequest.Host = "127.0.0.1:32147"
	addRequest.Header.Set("Content-Type", "application/json")
	addResponse := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(addResponse, addRequest)
	if addResponse.Code != http.StatusCreated {
		t.Fatalf("unexpected add response: %d %s", addResponse.Code, addResponse.Body.String())
	}
	if len(repository.games) != 1 || len(repository.paths) != 1 || coordinator.reconciled != 1 {
		t.Fatalf("manual game was not fully registered: games=%d paths=%d reconciled=%d", len(repository.games), len(repository.paths), coordinator.reconciled)
	}
	badRequest := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/games", strings.NewReader(`{"name":"Bad","paths":["relative"]}`))
	badRequest.Host = "127.0.0.1:32147"
	badResponse := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusBadRequest || len(repository.games) != 1 {
		t.Fatalf("invalid game caused partial state: status=%d games=%d", badResponse.Code, len(repository.games))
	}
}

func TestServerRejectsDNSRebindingAndCrossSiteRequests(t *testing.T) {
	t.Parallel()
	handler := localRequestsOnly(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))

	for name, requestHeaders := range map[string][2]string{
		"dns rebinding host": {"attacker.example", ""},
		"cross-site origin":  {"127.0.0.1:32147", "https://attacker.example"},
		"opaque origin":      {"127.0.0.1:32147", "null"},
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/sync", nil)
			request.Host = requestHeaders[0]
			request.Header.Set("Origin", requestHeaders[1])
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden {
				t.Fatalf("unsafe request returned %d", response.Code)
			}
		})
	}
}
