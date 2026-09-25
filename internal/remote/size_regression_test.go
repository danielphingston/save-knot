package remote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/saveknot/saveknot/internal/core"
)

func TestEnsureLocalRejectsBlobLargerThanManifestSize(t *testing.T) {
	t.Parallel()
	raw := []byte("content larger than the claimed manifest size")
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	compressed := encoder.EncodeAll(raw, nil)
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	repository := &downloadRepoFake{}
	snapshot := core.Snapshot{Files: []core.SnapshotFile{{Hash: hash, Size: 3}}}
	if err := NewSyncer(repository).EnsureLocal(context.Background(), objectReaderFake{data: compressed}, snapshot, root); err == nil {
		t.Fatal("oversized decompressed remote blob was accepted")
	}
	if repository.path != "" {
		t.Fatalf("oversized blob was indexed at %q", repository.path)
	}
	if _, err := os.Stat(filepath.Join(root, hash[:2], hash+".zst")); !os.IsNotExist(err) {
		t.Fatalf("oversized blob was installed locally: %v", err)
	}
}
