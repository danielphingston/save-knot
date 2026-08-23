package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

type remoteCatalogFake struct {
	objects []Object
	data    map[string][]byte
}

func (f *remoteCatalogFake) List(context.Context, string) ([]Object, error) {
	return f.objects, nil
}

func (f *remoteCatalogFake) Get(_ context.Context, key string) (io.ReadCloser, int64, error) {
	data := f.data[key]
	return io.NopCloser(bytes.NewReader(data)), int64(len(data)), nil
}

type reconciliationRepoFake struct {
	games     []core.Game
	snapshots []core.Snapshot
	existing  map[string]bool
}

func (r *reconciliationRepoFake) EnsureRemoteGame(_ context.Context, game core.Game) error {
	r.games = append(r.games, game)
	return nil
}

func (r *reconciliationRepoFake) ImportSnapshot(_ context.Context, snapshot core.Snapshot) error {
	r.snapshots = append(r.snapshots, snapshot)
	return nil
}

func (r *reconciliationRepoFake) HasSnapshot(_ context.Context, id string) (bool, error) {
	return r.existing[id], nil
}

func TestReconcilerImportsOnlySnapshotManifests(t *testing.T) {
	t.Parallel()
	hash := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	snapshot := core.Snapshot{
		Version: 1, ID: "snapshot-a", GameID: "game-a", GameName: "Game A", DeviceID: "device-a",
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(), Files: []core.SnapshotFile{{Path: "slot.sav", Hash: hash}},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &remoteCatalogFake{
		objects: []Object{
			{Key: "games/game-a/metadata.json", Size: 2},
			{Key: "games/game-a/snapshots/snapshot-a.json", Size: int64(len(data))},
		},
		data: map[string][]byte{"games/game-a/snapshots/snapshot-a.json": data},
	}
	repository := &reconciliationRepoFake{}
	result, err := NewReconciler(repository).Reconcile(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Objects != 2 || result.Snapshots != 1 || result.Games != 1 {
		t.Fatalf("unexpected reconciliation result: %#v", result)
	}
	if len(repository.games) != 1 || repository.games[0].Enabled || len(repository.snapshots) != 1 || repository.snapshots[0].RemoteState != "synced" {
		t.Fatalf("unexpected imported state: games=%#v snapshots=%#v", repository.games, repository.snapshots)
	}
}

func TestReconcilerDoesNotDownloadKnownImmutableSnapshots(t *testing.T) {
	t.Parallel()
	catalog := &remoteCatalogFake{objects: []Object{{Key: "games/game-a/snapshots/snapshot-a.json", Size: 100}}}
	repository := &reconciliationRepoFake{existing: map[string]bool{"snapshot-a": true}}
	result, err := NewReconciler(repository).Reconcile(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Snapshots != 0 || result.Games != 1 || len(repository.snapshots) != 0 {
		t.Fatalf("known snapshot was not skipped: result=%#v repository=%#v", result, repository)
	}
}

func TestReconcilerRejectsUnsafeRemotePaths(t *testing.T) {
	t.Parallel()
	snapshot := core.Snapshot{
		Version: 1, ID: "snapshot-a", GameID: "game-a", GameName: "Game A",
		Files: []core.SnapshotFile{{Path: "../outside", Hash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	catalog := &remoteCatalogFake{
		objects: []Object{{Key: "games/game-a/snapshots/snapshot-a.json", Size: int64(len(data))}},
		data:    map[string][]byte{"games/game-a/snapshots/snapshot-a.json": data},
	}
	if _, err := NewReconciler(&reconciliationRepoFake{}).Reconcile(context.Background(), catalog); err == nil {
		t.Fatal("unsafe remote file path was accepted")
	}
}
