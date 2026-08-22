package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/saveknot/saveknot/internal/core"
)

type objectWriter interface {
	Identity() string
	Put(context.Context, string, string, string, io.Reader, int64) error
}

type syncRepository interface {
	BlobPath(context.Context, string) (string, error)
	BlobUploaded(context.Context, string, string) (bool, error)
	MarkBlobUploaded(context.Context, string, string) error
	MarkSnapshotRemote(context.Context, string, string) error
}

type Syncer struct {
	repository syncRepository
}

func NewSyncer(repository syncRepository) *Syncer {
	return &Syncer{repository: repository}
}

func (s *Syncer) Upload(ctx context.Context, writer objectWriter, snapshot core.Snapshot) error {
	uploaded := make(map[string]struct{}, len(snapshot.Files))
	target := writer.Identity()
	for _, file := range snapshot.Files {
		if _, ok := uploaded[file.Hash]; ok {
			continue
		}
		if err := s.ensureBlob(ctx, writer, file.Hash, target); err != nil {
			return err
		}
		uploaded[file.Hash] = struct{}{}
	}
	manifest := snapshot
	manifest.RemoteState = "synced"
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode remote snapshot manifest: %w", err)
	}
	key := path.Join("games", snapshot.GameID, "snapshots", snapshot.ID+".json")
	if err := writer.Put(ctx, key, "application/json", "", bytes.NewReader(data), int64(len(data))); err != nil {
		return err
	}
	if err := s.repository.MarkSnapshotRemote(ctx, snapshot.ID, "synced"); err != nil {
		return err
	}
	return nil
}

func (s *Syncer) ensureBlob(ctx context.Context, writer objectWriter, hash, target string) error {
	alreadyUploaded, err := s.repository.BlobUploaded(ctx, hash, target)
	if err != nil || alreadyUploaded {
		return err
	}
	blobPath, err := s.repository.BlobPath(ctx, hash)
	if err != nil {
		return err
	}
	//nolint:gosec // blobPath is retrieved from SaveKnot's private blob index, not remote input.
	blob, err := os.Open(blobPath)
	if err != nil {
		return fmt.Errorf("open blob %q for upload: %w", hash, err)
	}
	info, err := blob.Stat()
	if err != nil {
		return fmt.Errorf("inspect blob %q: %w", hash, errors.Join(err, blob.Close()))
	}
	key := path.Join("blobs", "sha256", hash[:2], hash+".zst")
	uploadErr := writer.Put(ctx, key, "application/octet-stream", "zstd", blob, info.Size())
	closeErr := blob.Close()
	if uploadErr != nil {
		return uploadErr
	}
	if closeErr != nil {
		return fmt.Errorf("close blob %q after upload: %w", hash, closeErr)
	}
	return s.repository.MarkBlobUploaded(ctx, hash, target)
}
