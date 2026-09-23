package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

const maxRemoteManifestSize = 8 << 20

type remoteCatalog interface {
	List(context.Context, string) ([]Object, error)
	Get(context.Context, string) (io.ReadCloser, int64, error)
}

type reconciliationRepository interface {
	SaveActiveSelection(context.Context, core.ActiveSelection) error
	EnsureRemoteGame(context.Context, core.Game) error
	HasSnapshot(context.Context, string) (bool, error)
	ImportSnapshot(context.Context, core.Snapshot) error
}

type ReconcileResult struct {
	Objects   int `json:"objects"`
	Snapshots int `json:"snapshots"`
	Skipped   int `json:"skipped"`
	Games     int `json:"games"`
}

type Reconciler struct {
	repository reconciliationRepository
}

func NewReconciler(repository reconciliationRepository) *Reconciler {
	return &Reconciler{repository: repository}
}

func (r *Reconciler) Reconcile(ctx context.Context, catalog remoteCatalog) (ReconcileResult, error) {
	objects, err := catalog.List(ctx, "games/")
	if err != nil {
		return ReconcileResult{}, err
	}
	result := ReconcileResult{Objects: len(objects)}
	games := make(map[string]struct{})
	var selectionObjects []Object
	availableSnapshots := make(map[string]bool)
	for _, object := range objects {
		if _, _, ok := activeSelectionObjectIdentity(object.Key); ok {
			selectionObjects = append(selectionObjects, object)
			continue
		}
		if !strings.Contains(object.Key, "/snapshots/") || path.Ext(object.Key) != ".json" {
			continue
		}
		gameID, snapshotID, ok := snapshotObjectIdentity(object.Key)
		if !ok {
			return result, fmt.Errorf("remote snapshot object %q has an invalid key", object.Key)
		}
		exists, err := r.repository.HasSnapshot(ctx, snapshotID)
		if err != nil {
			return result, fmt.Errorf("check local snapshot %q: %w", snapshotID, err)
		}
		if exists {
			games[gameID] = struct{}{}
			availableSnapshots[snapshotID] = true
			result.Skipped++
			continue
		}
		snapshot, err := readRemoteSnapshot(ctx, catalog, object)
		if err != nil {
			return result, err
		}
		if snapshot.GameID != gameID || snapshot.ID != snapshotID {
			return result, fmt.Errorf("remote snapshot %q identity does not match its object key", object.Key)
		}
		now := time.Now().UTC()
		game := core.Game{
			ID: snapshot.GameID, CatalogID: snapshot.GameID, CatalogName: snapshot.GameName,
			DisplayName: firstMetadataName(snapshot), Image: snapshot.Metadata.Image, Notes: snapshot.Metadata.Notes,
			Store: "remote", Enabled: false, LastSeen: &now,
		}
		if err := r.repository.EnsureRemoteGame(ctx, game); err != nil {
			return result, fmt.Errorf("register remote game %q: %w", snapshot.GameID, err)
		}
		if err := r.repository.ImportSnapshot(ctx, snapshot); err != nil {
			return result, err
		}
		availableSnapshots[snapshot.ID] = true
		games[snapshot.GameID] = struct{}{}
		result.Snapshots++
	}
	for _, object := range selectionObjects {
		gameID, eventID, _ := activeSelectionObjectIdentity(object.Key)
		selection, err := readRemoteSelection(ctx, catalog, object)
		if err != nil {
			return result, err
		}
		if selection.GameID != gameID || selection.ID != eventID {
			return result, fmt.Errorf("remote active selection %q identity does not match its object key", object.Key)
		}
		// Deleting a snapshot leaves its immutable event object behind. Ignore stale
		// events for missing snapshots so a fresh device can still reconcile history.
		if !availableSnapshots[selection.SnapshotID] {
			continue
		}
		if err := r.repository.SaveActiveSelection(ctx, selection); err != nil {
			return result, fmt.Errorf("import active selection %q: %w", eventID, err)
		}
	}
	result.Games = len(games)
	return result, nil
}

func activeSelectionObjectIdentity(key string) (string, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "games" || parts[2] != "active-selections" || path.Ext(parts[3]) != ".json" {
		return "", "", false
	}
	gameID, eventID := parts[1], strings.TrimSuffix(parts[3], ".json")
	if !objectIDPattern.MatchString(gameID) || !objectIDPattern.MatchString(eventID) {
		return "", "", false
	}
	return gameID, eventID, true
}

func readRemoteSelection(ctx context.Context, catalog remoteCatalog, object Object) (core.ActiveSelection, error) {
	if object.Size > 1<<20 {
		return core.ActiveSelection{}, fmt.Errorf("remote active selection %q is too large", object.Key)
	}
	body, _, err := catalog.Get(ctx, object.Key)
	if err != nil {
		return core.ActiveSelection{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(body, (1<<20)+1))
	closeErr := body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return core.ActiveSelection{}, fmt.Errorf("read remote active selection: %w", err)
	}
	var selection core.ActiveSelection
	if len(data) > 1<<20 {
		return selection, fmt.Errorf("remote active selection %q is too large", object.Key)
	}
	if err := json.Unmarshal(data, &selection); err != nil {
		return selection, fmt.Errorf("decode remote active selection: %w", err)
	}
	if selection.ID == "" || selection.GameID == "" || selection.SnapshotID == "" || selection.DeviceID == "" || selection.SelectedAt.IsZero() {
		return selection, fmt.Errorf("remote active selection %q has invalid identity", object.Key)
	}
	return selection, nil
}

func snapshotObjectIdentity(key string) (string, string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "games" || parts[2] != "snapshots" || path.Ext(parts[3]) != ".json" {
		return "", "", false
	}
	gameID := parts[1]
	snapshotID := strings.TrimSuffix(parts[3], ".json")
	if !objectIDPattern.MatchString(gameID) || !objectIDPattern.MatchString(snapshotID) {
		return "", "", false
	}
	return gameID, snapshotID, true
}

func firstMetadataName(snapshot core.Snapshot) string {
	if snapshot.Metadata.DisplayName != "" {
		return snapshot.Metadata.DisplayName
	}
	return snapshot.GameName
}

func readRemoteSnapshot(ctx context.Context, catalog remoteCatalog, object Object) (core.Snapshot, error) {
	if object.Size > maxRemoteManifestSize {
		return core.Snapshot{}, fmt.Errorf("remote snapshot manifest %q exceeds %d bytes", object.Key, maxRemoteManifestSize)
	}
	body, _, err := catalog.Get(ctx, object.Key)
	if err != nil {
		return core.Snapshot{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(body, maxRemoteManifestSize+1))
	closeErr := body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return core.Snapshot{}, fmt.Errorf("read remote snapshot %q: %w", object.Key, err)
	}
	if len(data) > maxRemoteManifestSize {
		return core.Snapshot{}, fmt.Errorf("remote snapshot manifest %q exceeds %d bytes", object.Key, maxRemoteManifestSize)
	}
	var snapshot core.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return core.Snapshot{}, fmt.Errorf("decode remote snapshot %q: %w", object.Key, err)
	}
	if snapshot.Version != 1 || snapshot.ID == "" || snapshot.GameID == "" || snapshot.GameName == "" {
		return core.Snapshot{}, fmt.Errorf("remote snapshot %q has invalid identity or version", object.Key)
	}
	for _, file := range snapshot.Files {
		clean := path.Clean(file.Path)
		if (!file.RootFile && (clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(file.Path, "/"))) || !sha256Pattern.MatchString(file.Hash) {
			return core.Snapshot{}, fmt.Errorf("remote snapshot %q contains an unsafe file entry", object.Key)
		}
	}
	if snapshot.Registry != nil && !sha256Pattern.MatchString(snapshot.Registry.Hash) {
		return core.Snapshot{}, fmt.Errorf("remote snapshot %q contains an unsafe registry entry", object.Key)
	}
	snapshot.RemoteState = "synced"
	return snapshot, nil
}

var (
	sha256Pattern   = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
	objectIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)
