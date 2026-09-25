package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
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
	SaveBlob(context.Context, string, string, int64, int64) error
}

type objectReader interface {
	Get(context.Context, string) (io.ReadCloser, int64, error)
}

type Syncer struct {
	repository syncRepository
}

func (s *Syncer) EnsureLocal(ctx context.Context, reader objectReader, snapshot core.Snapshot, blobRoot string) error {
	originalSizes := make(map[string]int64)
	for _, file := range snapshot.Files {
		originalSizes[file.Hash] = file.Size
	}
	if snapshot.Registry != nil {
		originalSizes[snapshot.Registry.Hash] = snapshot.Registry.Size
	}
	for hashValue, originalSize := range originalSizes {
		if err := s.ensureLocalBlob(ctx, reader, hashValue, originalSize, blobRoot); err != nil {
			return err
		}
	}
	return nil
}

func (s *Syncer) NeedsDownload(ctx context.Context, snapshot core.Snapshot) (bool, error) {
	seen := make(map[string]struct{})
	needsDownload := false
	for _, file := range snapshot.Files {
		if _, ok := seen[file.Hash]; ok {
			continue
		}
		seen[file.Hash] = struct{}{}
		blobPath, err := s.repository.BlobPath(ctx, file.Hash)
		if err == nil && verifyCompressedBlob(blobPath, file.Hash, file.Size) != nil {
			needsDownload = true
			continue
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			needsDownload = true
		}
	}
	if snapshot.Registry != nil {
		blobPath, err := s.repository.BlobPath(ctx, snapshot.Registry.Hash)
		if err == nil && verifyCompressedBlob(blobPath, snapshot.Registry.Hash, snapshot.Registry.Size) != nil {
			needsDownload = true
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			needsDownload = true
		}
	}
	return needsDownload, nil
}

func (s *Syncer) ensureLocalBlob(ctx context.Context, reader objectReader, hashValue string, originalSize int64, blobRoot string) error {
	existing, err := s.repository.BlobPath(ctx, hashValue)
	if err == nil {
		if verifyErr := verifyCompressedBlob(existing, hashValue, originalSize); verifyErr == nil {
			return nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(hashValue) != sha256.Size*2 {
		return fmt.Errorf("invalid remote blob hash %q", hashValue)
	}
	key := path.Join("blobs", "sha256", hashValue[:2], hashValue+".zst")
	body, size, err := reader.Get(ctx, key)
	if err != nil {
		return err
	}
	staging := filepath.Join(blobRoot, ".staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return errors.Join(fmt.Errorf("create blob download staging directory: %w", err), body.Close())
	}
	temporary, err := os.CreateTemp(staging, "remote-blob-*.zst")
	if err != nil {
		return errors.Join(fmt.Errorf("create staged remote blob: %w", err), body.Close())
	}
	temporaryPath := temporary.Name()
	written, copyErr := io.Copy(temporary, body)
	closeErr := errors.Join(body.Close(), temporary.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("download remote blob %q: %w", hashValue, errors.Join(err, removeFile(temporaryPath)))
	}
	if size > 0 && written != size {
		return errors.Join(fmt.Errorf("remote blob %q was truncated: expected %d bytes, received %d", hashValue, size, written), removeFile(temporaryPath))
	}
	if err := verifyCompressedBlob(temporaryPath, hashValue, originalSize); err != nil {
		return errors.Join(err, removeFile(temporaryPath))
	}
	directory := filepath.Join(blobRoot, hashValue[:2])
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return errors.Join(fmt.Errorf("create local blob directory: %w", err), removeFile(temporaryPath))
	}
	destination := filepath.Join(directory, hashValue+".zst")
	if _, err := os.Stat(destination); err == nil {
		invalid := destination + ".invalid-" + time.Now().UTC().Format("20060102T150405.000000000")
		if err := os.Rename(destination, invalid); err != nil {
			return errors.Join(fmt.Errorf("preserve invalid local blob: %w", err), removeFile(temporaryPath))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.Join(fmt.Errorf("inspect local blob destination: %w", err), removeFile(temporaryPath))
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("store downloaded blob: %w", errors.Join(err, removeFile(temporaryPath)))
	}
	return s.repository.SaveBlob(ctx, hashValue, destination, originalSize, written)
}

func verifyCompressedBlob(blobPath, expectedHash string, expectedSize int64) error {
	if expectedSize < 0 {
		return errors.New("compressed blob has an invalid original size")
	}
	//nolint:gosec // The path comes from SaveKnot's private blob index or staging directory.
	file, err := os.Open(blobPath)
	if err != nil {
		return err
	}
	decoder, err := zstd.NewReader(file)
	if err != nil {
		return errors.Join(fmt.Errorf("decode downloaded blob: %w", err), file.Close())
	}
	digest := sha256.New()
	written, copyErr := io.Copy(digest, io.LimitReader(decoder, expectedSize+1))
	decoder.Close()
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return fmt.Errorf("verify downloaded blob: %w", err)
	}
	if written != expectedSize {
		return errors.New("downloaded blob size did not match the snapshot")
	}
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expectedHash) {
		return errors.New("downloaded blob failed SHA-256 verification")
	}
	return nil
}

func removeFile(filePath string) error {
	err := os.Remove(filePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
	if snapshot.Registry != nil {
		if _, ok := uploaded[snapshot.Registry.Hash]; !ok {
			if err := s.ensureBlob(ctx, writer, snapshot.Registry.Hash, target); err != nil {
				return err
			}
		}
	}
	metadata, err := json.Marshal(snapshot.Metadata)
	if err != nil {
		return fmt.Errorf("encode portable game metadata: %w", err)
	}
	metadataKey := path.Join("games", snapshot.GameID, "metadata.json")
	if err := writer.Put(ctx, metadataKey, "application/json", "", bytes.NewReader(metadata), int64(len(metadata))); err != nil {
		return err
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
	if len(hash) != sha256.Size*2 {
		return fmt.Errorf("invalid local blob hash %q", hash)
	}
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
