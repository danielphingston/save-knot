package remote

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

type syncRepoFake struct {
	path        string
	uploaded    []string
	uploadedTo  string
	remoteID    string
	remoteState string
}

func (r *syncRepoFake) BlobPath(context.Context, string) (string, error) { return r.path, nil }
func (r *syncRepoFake) BlobUploaded(_ context.Context, _ string, target string) (bool, error) {
	return r.uploadedTo == target, nil
}

func (r *syncRepoFake) MarkBlobUploaded(_ context.Context, hash, target string) error {
	r.uploaded = append(r.uploaded, hash)
	r.uploadedTo = target
	return nil
}

func (r *syncRepoFake) MarkSnapshotRemote(_ context.Context, id, state string) error {
	r.remoteID, r.remoteState = id, state
	return nil
}
func (r *syncRepoFake) SaveBlob(context.Context, string, string, int64, int64) error { return nil }

type objectWriterFake struct {
	keys   []string
	bodies [][]byte
}

func (*objectWriterFake) Identity() string { return "account/bucket/saveknot" }

func (w *objectWriterFake) Put(_ context.Context, key, _, _ string, body io.Reader, _ int64) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	w.keys = append(w.keys, key)
	w.bodies = append(w.bodies, data)
	return nil
}

func TestSyncUploadsUniqueBlobsBeforeManifest(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "blob.zst")
	if err := os.WriteFile(path, []byte("compressed"), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	repository := &syncRepoFake{path: path}
	writer := &objectWriterFake{}
	snapshot := core.Snapshot{ID: "snapshot-a", GameID: "game-a", CreatedAt: time.Now(), Files: []core.SnapshotFile{{Hash: hash}, {Hash: hash}}, RemoteState: "local"}
	if err := NewSyncer(repository).Upload(context.Background(), writer, snapshot); err != nil {
		t.Fatal(err)
	}
	if len(writer.keys) != 3 || writer.keys[0] != "blobs/sha256/ab/"+hash+".zst" || writer.keys[1] != "games/game-a/metadata.json" || writer.keys[2] != "games/game-a/snapshots/snapshot-a.json" {
		t.Fatalf("unexpected upload order: %#v", writer.keys)
	}
	if len(repository.uploaded) != 1 || repository.remoteID != snapshot.ID || repository.remoteState != "synced" {
		t.Fatalf("unexpected repository state: %#v", repository)
	}
	if !bytes.Contains(writer.bodies[2], []byte(`"remoteState":"synced"`)) {
		t.Fatal("remote manifest was not marked synced")
	}
	if err := NewSyncer(repository).Upload(context.Background(), writer, snapshot); err != nil {
		t.Fatal(err)
	}
	if len(writer.keys) != 5 || writer.keys[3] != writer.keys[1] || writer.keys[4] != writer.keys[2] || len(repository.uploaded) != 1 {
		t.Fatalf("already uploaded blob was sent again: keys=%#v uploads=%#v", writer.keys, repository.uploaded)
	}
}
