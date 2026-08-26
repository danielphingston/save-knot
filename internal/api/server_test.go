package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
func (r *repositoryFake) ListHiddenGames(context.Context) ([]core.Game, error) {
	var games []core.Game
	for _, game := range r.games {
		if game.Hidden {
			games = append(games, game)
		}
	}
	return games, nil
}

func (r *repositoryFake) Game(_ context.Context, id string) (core.Game, error) {
	for _, game := range r.games {
		if game.ID == id {
			return game, nil
		}
	}
	return core.Game{}, core.ErrGameNotFound
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

func (r *repositoryFake) UpdateGame(_ context.Context, id string, update core.GameUpdate) error {
	for index := range r.games {
		if r.games[index].ID != id {
			continue
		}
		if update.DisplayName != nil {
			r.games[index].DisplayName = *update.DisplayName
		}
		if update.Image != nil {
			r.games[index].Image = *update.Image
		}
		if update.Notes != nil {
			r.games[index].Notes = *update.Notes
		}
		if update.Enabled != nil {
			r.games[index].Enabled = *update.Enabled
		}
		if update.SyncEnabled != nil {
			r.games[index].SyncEnabled = *update.SyncEnabled
		}
		return nil
	}
	return core.ErrGameNotFound
}
func (r *repositoryFake) UpdatePath(context.Context, string, string, bool) error { return nil }
func (r *repositoryFake) DeletePath(context.Context, string, string) error       { return nil }
func (r *repositoryFake) AddExclusion(context.Context, core.GameExclusion) error { return nil }
func (r *repositoryFake) DeleteExclusion(context.Context, string, string) error  { return nil }
func (r *repositoryFake) UpdateGamePolicy(context.Context, string, core.BackupPolicy) error {
	return nil
}

type coordinatorFake struct {
	reconciled int
	repository *repositoryFake
	syncResult core.SyncResult
	syncErr    error
	syncActive bool
}

func (c *coordinatorFake) Backup(context.Context, string) (core.Snapshot, error) {
	return core.Snapshot{}, nil
}

func (c *coordinatorFake) Restore(context.Context, string, string) (core.Snapshot, error) {
	return core.Snapshot{}, nil
}
func (c *coordinatorFake) ReconcileWatches(context.Context) { c.reconciled++ }
func (c *coordinatorFake) SyncNow(context.Context) (core.SyncResult, error) {
	return c.syncResult, c.syncErr
}
func (c *coordinatorFake) SyncInProgress() bool                                       { return c.syncActive }
func (c *coordinatorFake) SyncGame(context.Context, string) error                     { return nil }
func (c *coordinatorFake) DeleteSnapshot(context.Context, string, string, bool) error { return nil }
func (c *coordinatorFake) SetGameHidden(_ context.Context, id string, hidden bool) error {
	if c.repository == nil {
		return nil
	}
	for index := range c.repository.games {
		if c.repository.games[index].ID == id {
			c.repository.games[index].Hidden = hidden
			c.reconciled++
			return nil
		}
	}
	return core.ErrGameNotFound
}
func (c *coordinatorFake) DiscoverNow(context.Context) core.Diagnostics       { return core.Diagnostics{} }
func (c *coordinatorFake) ConfigureLocal(context.Context, config.Local) error { return nil }
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
func (v *vaultFake) Delete(id string) error        { delete(v.values, id); return nil }

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
	if statusResponse.Code != http.StatusOK || statusResponse.Header().Get("Content-Security-Policy") == "" || statusResponse.Header().Get("Cache-Control") != "no-store" {
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

//nolint:gocognit // This integration test intentionally verifies the complete HTTP lifecycle in one server instance.
func TestGameLifecycleRecoveryAndSettingsAPI(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	vault := &vaultFake{values: map[string]string{"r2-default": "secret"}}
	manager, err := settings.New(filepath.Join(directory, "config.json"), config.Config{
		Listen: "127.0.0.1:32147", DeviceID: "device-a", RetentionKeep: 50,
		R2: config.R2{AccountID: "account", Bucket: "bucket", AccessKeyID: "key", CredentialID: "r2-default"},
	}, vault)
	if err != nil {
		t.Fatal(err)
	}
	repository := &repositoryFake{games: []core.Game{{ID: "game-a", DisplayName: "Game A", Store: "custom", Enabled: true, SyncEnabled: true}}}
	coordinator := &coordinatorFake{repository: repository}
	server, err := New("127.0.0.1:32147", repository, repository, repository, repository, coordinator, coordinator, coordinator, manager, events.New(), filepath.Join(directory, "artwork"))
	if err != nil {
		t.Fatal(err)
	}

	detail := serveRequest(server, http.MethodGet, "/api/games/game-a", "")
	if detail.Code != http.StatusOK {
		t.Fatalf("game detail failed: %d %s", detail.Code, detail.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(detail.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"paths", "registry", "exclusions"} {
		if string(payload[field]) != "[]" {
			t.Fatalf("%s was not normalized to an array: %s", field, payload[field])
		}
	}

	missing := serveRequest(server, http.MethodGet, "/api/games/missing", "")
	if missing.Code != http.StatusNotFound || strings.Contains(strings.ToLower(missing.Body.String()), "sql") || !strings.Contains(missing.Body.String(), "game not found") {
		t.Fatalf("missing game leaked internals or wrong status: %d %s", missing.Code, missing.Body.String())
	}

	hidden := serveRequest(server, http.MethodDelete, "/api/games/game-a", "")
	if hidden.Code != http.StatusNoContent || !repository.games[0].Hidden {
		t.Fatalf("game was not hidden: %d game=%#v", hidden.Code, repository.games[0])
	}
	ignored := serveRequest(server, http.MethodGet, "/api/games/ignored", "")
	if ignored.Code != http.StatusOK || !strings.Contains(ignored.Body.String(), "Game A") {
		t.Fatalf("ignored game was not listed: %d %s", ignored.Code, ignored.Body.String())
	}
	restored := serveRequest(server, http.MethodPost, "/api/games/game-a/restore-library", `{}`)
	if restored.Code != http.StatusNoContent || repository.games[0].Hidden {
		t.Fatalf("game was not restored: %d game=%#v", restored.Code, repository.games[0])
	}

	retention := serveRequest(server, http.MethodPut, "/api/settings/retention", `{"keep":51}`)
	if retention.Code != http.StatusNoContent {
		t.Fatalf("retention update failed: %d %s", retention.Code, retention.Body.String())
	}
	automation := serveRequest(server, http.MethodPut, "/api/settings/automation", `{"periodicSyncEnabled":false,"syncIntervalMinutes":30,"periodicDiscoveryEnabled":true,"discoveryIntervalMinutes":120}`)
	if automation.Code != http.StatusNoContent {
		t.Fatalf("automation update failed: %d %s", automation.Code, automation.Body.String())
	}
	status := serveRequest(server, http.MethodGet, "/api/status", "")
	if !strings.Contains(status.Body.String(), `"retentionKeep":51`) || !strings.Contains(status.Body.String(), `"periodicDiscoveryEnabled":true`) || !strings.Contains(status.Body.String(), `"r2State":"configured"`) {
		t.Fatalf("status did not expose persisted settings: %s", status.Body.String())
	}
	manager.RecordR2Success()
	status = serveRequest(server, http.MethodGet, "/api/status", "")
	if !strings.Contains(status.Body.String(), `"r2State":"verified"`) || strings.Contains(status.Body.String(), `"r2LastVerified":null`) {
		t.Fatalf("verified R2 status was not exposed: %s", status.Body.String())
	}
	manager.RecordR2Sync()
	status = serveRequest(server, http.MethodGet, "/api/status", "")
	if strings.Contains(status.Body.String(), `"r2LastSynced":null`) {
		t.Fatalf("last R2 sync time was not exposed: %s", status.Body.String())
	}
	manager.RecordR2Failure(errors.New("bucket unavailable"))
	status = serveRequest(server, http.MethodGet, "/api/status", "")
	if !strings.Contains(status.Body.String(), `"r2State":"unavailable"`) || !strings.Contains(status.Body.String(), "bucket unavailable") {
		t.Fatalf("degraded R2 status was not exposed: %s", status.Body.String())
	}
	disconnect := serveRequest(server, http.MethodDelete, "/api/r2", "")
	if disconnect.Code != http.StatusNoContent {
		t.Fatalf("R2 disconnect failed: %d %s", disconnect.Code, disconnect.Body.String())
	}
	status = serveRequest(server, http.MethodGet, "/api/status", "")
	if !strings.Contains(status.Body.String(), `"r2Configured":false`) || !strings.Contains(status.Body.String(), `"r2State":"disconnected"`) {
		t.Fatalf("disconnected status remained active: %s", status.Body.String())
	}
}

func TestResetImageRestoresDefaultAndRemovesOnlyCustomArtwork(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	artwork := filepath.Join(directory, "artwork")
	if err := os.MkdirAll(artwork, 0o700); err != nil {
		t.Fatal(err)
	}
	customPath := filepath.Join(artwork, "game-a.png")
	if err := os.WriteFile(customPath, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(artwork, "other.png")
	if err := os.WriteFile(unrelated, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := settings.New(filepath.Join(directory, "config.json"), config.Config{Listen: "127.0.0.1:32147", DeviceID: "device-a"}, &vaultFake{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	repository := &repositoryFake{games: []core.Game{{ID: "game-a", DisplayName: "Game A", Store: "steam", StoreID: "123", Image: "/artwork/game-a.png"}}}
	coordinator := &coordinatorFake{repository: repository}
	server, err := New("127.0.0.1:32147", repository, repository, repository, repository, coordinator, coordinator, coordinator, manager, events.New(), artwork)
	if err != nil {
		t.Fatal(err)
	}
	response := serveRequest(server, http.MethodDelete, "/api/games/game-a/image", "")
	if response.Code != http.StatusNoContent {
		t.Fatalf("reset image failed: %d %s", response.Code, response.Body.String())
	}
	if repository.games[0].Image != defaultGameImage(repository.games[0]) {
		t.Fatalf("default artwork was not restored: %q", repository.games[0].Image)
	}
	if _, err := os.Stat(customPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("custom artwork remained: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("unrelated artwork was removed: %v", err)
	}
}

func TestSyncAllReportsPartialCompletion(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	manager, err := settings.New(filepath.Join(directory, "config.json"), config.Config{Listen: "127.0.0.1:32147", DeviceID: "device-a"}, &vaultFake{values: make(map[string]string)})
	if err != nil {
		t.Fatal(err)
	}
	repository := &repositoryFake{}
	coordinator := &coordinatorFake{syncResult: core.SyncResult{Eligible: 2, Synced: 1, Failed: 1}, syncErr: errors.New("one upload failed")}
	server, err := New("127.0.0.1:32147", repository, repository, repository, repository, coordinator, coordinator, coordinator, manager, events.New(), filepath.Join(directory, "artwork"))
	if err != nil {
		t.Fatal(err)
	}
	response := serveRequest(server, http.MethodPost, "/api/sync", `{}`)
	if response.Code != http.StatusMultiStatus || !strings.Contains(response.Body.String(), `"synced":1`) || !strings.Contains(response.Body.String(), `"failed":1`) || !strings.Contains(response.Body.String(), "one upload failed") {
		t.Fatalf("partial sync result was not reported: %d %s", response.Code, response.Body.String())
	}
	coordinator.syncErr = core.ErrSyncInProgress
	response = serveRequest(server, http.MethodPost, "/api/sync", `{}`)
	if response.Code != http.StatusConflict {
		t.Fatalf("concurrent sync returned %d: %s", response.Code, response.Body.String())
	}
	coordinator.syncActive = true
	status := serveRequest(server, http.MethodGet, "/api/status", "")
	if !strings.Contains(status.Body.String(), `"syncInProgress":true`) {
		t.Fatalf("active sync was not exposed: %s", status.Body.String())
	}
}

func serveRequest(server *Server, method, target, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(body))
	request.Host = "127.0.0.1:32147"
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	server.http.Handler.ServeHTTP(response, request)
	return response
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
