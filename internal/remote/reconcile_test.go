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
	games      []core.Game
	snapshots  []core.Snapshot
	selections []core.ActiveSelection
	existing   map[string]bool
}

func (r *reconciliationRepoFake) SaveActiveSelection(_ context.Context, selection core.ActiveSelection) error {
	r.selections = append(r.selections, selection)
	return nil
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

func TestReconcilerImportsActiveSelectionEventsWithoutRemovingVersions(t *testing.T) {
	t.Parallel()
	selection := core.ActiveSelection{ID: "event-b", GameID: "game-a", SnapshotID: "snapshot-b", DeviceID: "computer-b", SelectedAt: time.Unix(1_700_000_000, 0).UTC()}
	selectionData, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := core.Snapshot{Version: 1, ID: "snapshot-b", GameID: "game-a", GameName: "Game A", DeviceID: "computer-a", CreatedAt: time.Unix(1_699_999_000, 0).UTC(), Files: []core.SnapshotFile{}}
	snapshotData, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	selectionKey := "games/game-a/active-selections/event-b.json"
	snapshotKey := "games/game-a/snapshots/snapshot-b.json"
	catalog := &remoteCatalogFake{
		objects: []Object{{Key: selectionKey, Size: int64(len(selectionData))}, {Key: snapshotKey, Size: int64(len(snapshotData))}},
		data:    map[string][]byte{selectionKey: selectionData, snapshotKey: snapshotData},
	}
	repository := &reconciliationRepoFake{}
	result, err := NewReconciler(repository).Reconcile(context.Background(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	if result.Snapshots != 1 || len(repository.snapshots) != 1 || len(repository.selections) != 1 || repository.selections[0] != selection {
		t.Fatalf("remote selection or its backup version was not imported: result=%#v snapshots=%#v selections=%#v", result, repository.snapshots, repository.selections)
	}
}

func TestReconcilerIgnoresOrphanedActiveSelectionEvent(t *testing.T) {
	t.Parallel()
	selection := core.ActiveSelection{ID: "event-old", GameID: "game-a", SnapshotID: "snapshot-deleted", DeviceID: "computer-b", SelectedAt: time.Unix(1_700_000_000, 0).UTC()}
	data, err := json.Marshal(selection)
	if err != nil {
		t.Fatal(err)
	}
	key := "games/game-a/active-selections/event-old.json"
	catalog := &remoteCatalogFake{objects: []Object{{Key: key, Size: int64(len(data))}}, data: map[string][]byte{key: data}}
	repository := &reconciliationRepoFake{}
	if _, err := NewReconciler(repository).Reconcile(context.Background(), catalog); err != nil {
		t.Fatalf("orphan selection blocked reconciliation: %v", err)
	}
	if len(repository.selections) != 0 {
		t.Fatalf("orphan event was imported: %#v", repository.selections)
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
