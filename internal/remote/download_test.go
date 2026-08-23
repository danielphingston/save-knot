package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/saveknot/saveknot/internal/core"
)

type downloadRepoFake struct {
	path string
}

func (r *downloadRepoFake) BlobPath(context.Context, string) (string, error) {
	if r.path == "" {
		return "", sql.ErrNoRows
	}
	return r.path, nil
}

func (*downloadRepoFake) BlobUploaded(context.Context, string, string) (bool, error) {
	return false, nil
}
func (*downloadRepoFake) MarkBlobUploaded(context.Context, string, string) error   { return nil }
func (*downloadRepoFake) MarkSnapshotRemote(context.Context, string, string) error { return nil }
func (r *downloadRepoFake) SaveBlob(_ context.Context, _ string, path string, _, _ int64) error {
	r.path = path
	return nil
}

type objectReaderFake struct {
	data []byte
}

func (r objectReaderFake) Get(context.Context, string) (io.ReadCloser, int64, error) {
	return io.NopCloser(bytes.NewReader(r.data)), int64(len(r.data)), nil
}

func TestEnsureLocalDownloadsAndVerifiesBlob(t *testing.T) {
	t.Parallel()
	raw := []byte("remote save contents")
	digest := sha256.Sum256(raw)
	hashValue := hex.EncodeToString(digest[:])
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(raw, nil)
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	repository := &downloadRepoFake{}
	syncer := NewSyncer(repository)
	snapshot := core.Snapshot{Files: []core.SnapshotFile{{Hash: hashValue, Size: int64(len(raw))}}}
	needed, err := syncer.NeedsDownload(context.Background(), snapshot)
	if err != nil || !needed {
		t.Fatalf("missing blob was not detected: needed=%v err=%v", needed, err)
	}
	if err := syncer.EnsureLocal(context.Background(), objectReaderFake{data: compressed}, snapshot, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if repository.path == "" || filepath.Ext(repository.path) != ".zst" {
		t.Fatalf("downloaded blob was not indexed: %q", repository.path)
	}
	if err := verifyCompressedBlob(repository.path, hashValue); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(repository.path); err != nil {
		t.Fatal(fmt.Errorf("inspect downloaded blob: %w", err))
	}
}
