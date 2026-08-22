package snapshot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/saveknot/saveknot/internal/core"
)

var (
	ErrNoFiles     = errors.New("no save files found")
	errFileChanged = errors.New("save file changed while it was being read")
)

type repository interface {
	GamePaths(context.Context, string) ([]core.GamePath, error)
	SaveBlob(context.Context, string, string, int64, int64) error
	BlobPath(context.Context, string) (string, error)
	SaveSnapshot(context.Context, core.Snapshot) error
	Snapshot(context.Context, string) (core.Snapshot, error)
}

type Service struct {
	repository repository
	blobRoot   string
	deviceID   string
	now        func() time.Time
}

func New(repository repository, blobRoot, deviceID string) *Service {
	return &Service{repository: repository, blobRoot: blobRoot, deviceID: deviceID, now: time.Now}
}

func (s *Service) Create(ctx context.Context, game core.Game) (core.Snapshot, error) {
	paths, err := s.repository.GamePaths(ctx, game.ID)
	if err != nil {
		return core.Snapshot{}, err
	}
	created := s.now().UTC()
	id, err := core.NewID(created)
	if err != nil {
		return core.Snapshot{}, err
	}
	snapshot := core.Snapshot{
		Version: 1, ID: id, GameID: game.ID, GameName: game.DisplayName,
		DeviceID: s.deviceID, CreatedAt: created, RemoteState: "local",
	}
	storedBlobs := make(map[string]int64)
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		if err := s.addPath(ctx, path, &snapshot, storedBlobs); err != nil {
			return core.Snapshot{}, err
		}
	}
	if len(snapshot.Files) == 0 {
		return core.Snapshot{}, ErrNoFiles
	}
	for _, size := range storedBlobs {
		snapshot.StoredSize += size
	}
	if err := s.repository.SaveSnapshot(ctx, snapshot); err != nil {
		return core.Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Service) addPath(ctx context.Context, path core.GamePath, snapshot *core.Snapshot, storedBlobs map[string]int64) error {
	files, err := filesAt(path.Resolved)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("scan save path %q: %w", path.Resolved, err)
	}
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		stored, err := s.storeStableFile(ctx, file.absolute)
		if err != nil {
			return fmt.Errorf("snapshot %q: %w", file.absolute, err)
		}
		if err := s.repository.SaveBlob(ctx, stored.hash, stored.path, stored.originalSize, stored.storedSize); err != nil {
			return err
		}
		snapshot.Files = append(snapshot.Files, core.SnapshotFile{
			SourceKey: sourceKey(path), Path: filepath.ToSlash(file.relative), RootFile: file.rootFile,
			Hash: stored.hash, Size: stored.originalSize, Mode: uint32(file.mode.Perm()), Modified: file.modified.UTC(),
		})
		snapshot.OriginalSize += stored.originalSize
		storedBlobs[stored.hash] = stored.storedSize
	}
	return nil
}

func (s *Service) storeStableFile(ctx context.Context, path string) (blob, error) {
	var lastErr error
	for range 3 {
		stored, err := s.storeFile(path)
		if !errors.Is(err, errFileChanged) {
			return stored, err
		}
		lastErr = err
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return blob{}, ctx.Err()
		case <-timer.C:
		}
	}
	return blob{}, lastErr
}

type scannedFile struct {
	absolute string
	relative string
	rootFile bool
	mode     fs.FileMode
	modified time.Time
}

func filesAt(root string) ([]scannedFile, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []scannedFile{{absolute: root, relative: filepath.Base(root), rootFile: true, mode: info.Mode(), modified: info.ModTime()}}, nil
	}
	var files []scannedFile
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, scannedFile{absolute: path, relative: relative, mode: info.Mode(), modified: info.ModTime()})
		return nil
	})
	return files, err
}

type blob struct {
	hash         string
	path         string
	originalSize int64
	storedSize   int64
}

func (s *Service) storeFile(path string) (result blob, err error) {
	before, err := os.Stat(path)
	if err != nil {
		return blob{}, err
	}
	if err := os.MkdirAll(filepath.Join(s.blobRoot, ".staging"), 0o700); err != nil {
		return blob{}, fmt.Errorf("create blob staging directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Join(s.blobRoot, ".staging"), "blob-*.zst")
	if err != nil {
		return blob{}, fmt.Errorf("create staged blob: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		if !keep {
			err = errors.Join(err, removeTemporary(temporaryPath))
		}
	}()
	//nolint:gosec // Reading the user-configured save path is this operation's explicit purpose.
	input, err := os.Open(path)
	if err != nil {
		return blob{}, errors.Join(err, temporary.Close())
	}
	digest := sha256.New()
	encoder, err := zstd.NewWriter(temporary, zstd.WithEncoderLevel(zstd.SpeedFastest))
	if err != nil {
		return blob{}, fmt.Errorf("create zstd encoder: %w", errors.Join(err, input.Close(), temporary.Close()))
	}
	written, copyErr := copyWithHash(encoder, digest, input)
	closeErr := errors.Join(input.Close(), encoder.Close(), temporary.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		return blob{}, fmt.Errorf("compress save file: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return blob{}, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || written != after.Size() {
		return blob{}, errFileChanged
	}
	hashValue := hex.EncodeToString(digest.Sum(nil))
	directory := filepath.Join(s.blobRoot, hashValue[:2])
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return blob{}, fmt.Errorf("create blob directory: %w", err)
	}
	destination := filepath.Join(directory, hashValue+".zst")
	if info, err := os.Stat(destination); err == nil {
		return blob{hash: hashValue, path: destination, originalSize: written, storedSize: info.Size()}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return blob{}, fmt.Errorf("check existing blob: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return blob{}, fmt.Errorf("store blob: %w", err)
	}
	keep = true
	info, err := os.Stat(destination)
	if err != nil {
		return blob{}, fmt.Errorf("inspect stored blob: %w", err)
	}
	return blob{hash: hashValue, path: destination, originalSize: written, storedSize: info.Size()}, nil
}

func copyWithHash(destination io.Writer, digest hash.Hash, source io.Reader) (int64, error) {
	return io.Copy(io.MultiWriter(destination, digest), source)
}

func sourceKey(path core.GamePath) string {
	digest := sha256.Sum256([]byte(path.Source + "\x00" + path.Template))
	return hex.EncodeToString(digest[:12])
}

func (s *Service) Restore(ctx context.Context, game core.Game, snapshotID string) (core.Snapshot, error) {
	target, err := s.repository.Snapshot(ctx, snapshotID)
	if err != nil {
		return core.Snapshot{}, err
	}
	if target.GameID != game.ID {
		return core.Snapshot{}, errors.New("snapshot does not belong to this game")
	}
	preRestore, err := s.Create(ctx, game)
	if err != nil && !errors.Is(err, ErrNoFiles) {
		return core.Snapshot{}, fmt.Errorf("create pre-restore snapshot: %w", err)
	}
	paths, err := s.repository.GamePaths(ctx, game.ID)
	if err != nil {
		return core.Snapshot{}, err
	}
	sources := make(map[string]core.GamePath)
	for _, path := range paths {
		if path.Enabled {
			sources[sourceKey(path)] = path
		}
	}
	for _, file := range target.Files {
		if err := ctx.Err(); err != nil {
			return core.Snapshot{}, err
		}
		source, ok := sources[file.SourceKey]
		if !ok {
			return core.Snapshot{}, fmt.Errorf("save location for %q is not configured on this device", file.Path)
		}
		destination := filepath.Join(source.Resolved, filepath.FromSlash(file.Path))
		if file.RootFile {
			destination = source.Resolved
		}
		if err := s.restoreFile(ctx, destination, file); err != nil {
			return core.Snapshot{}, err
		}
	}
	return preRestore, nil
}

func (s *Service) restoreFile(ctx context.Context, destination string, file core.SnapshotFile) error {
	blobPath, err := s.repository.BlobPath(ctx, file.Hash)
	if err != nil {
		return err
	}
	//nolint:gosec // blobPath is retrieved from SaveKnot's private blob index.
	input, err := os.Open(blobPath)
	if err != nil {
		return fmt.Errorf("open blob %q: %w", file.Hash, err)
	}
	decoder, err := zstd.NewReader(input)
	if err != nil {
		return fmt.Errorf("decode blob %q: %w", file.Hash, errors.Join(err, input.Close()))
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		decoder.Close()
		return fmt.Errorf("create restore directory: %w", errors.Join(err, input.Close()))
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".saveknot-restore-*")
	if err != nil {
		decoder.Close()
		return fmt.Errorf("create restore file: %w", errors.Join(err, input.Close()))
	}
	temporaryPath := temporary.Name()
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(temporary, digest), decoder)
	decoder.Close()
	closeErr := errors.Join(input.Close(), temporary.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		return fmt.Errorf("write restored file: %w", errors.Join(err, removeTemporary(temporaryPath)))
	}
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), file.Hash) {
		return errors.Join(errors.New("restored blob failed hash verification"), removeTemporary(temporaryPath))
	}
	if err := os.Chmod(temporaryPath, fs.FileMode(file.Mode)); err != nil {
		return fmt.Errorf("restore file permissions: %w", errors.Join(err, removeTemporary(temporaryPath)))
	}
	if err := os.Chtimes(temporaryPath, file.Modified, file.Modified); err != nil {
		return fmt.Errorf("restore file timestamp: %w", errors.Join(err, removeTemporary(temporaryPath)))
	}
	return replaceFile(destination, temporaryPath)
}

func replaceFile(destination, temporary string) error {
	backupID, err := core.NewID(time.Now().UTC())
	if err != nil {
		return errors.Join(err, removeTemporary(temporary))
	}
	backup := destination + ".saveknot-old-" + backupID
	if _, err := os.Stat(destination); err == nil {
		if err := os.Rename(destination, backup); err != nil {
			return fmt.Errorf("preserve current save: %w", errors.Join(err, removeTemporary(temporary)))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect restore destination: %w", errors.Join(err, removeTemporary(temporary)))
	}
	if err := os.Rename(temporary, destination); err != nil {
		rollbackErr := os.Rename(backup, destination)
		if errors.Is(rollbackErr, os.ErrNotExist) {
			rollbackErr = nil
		}
		return fmt.Errorf("activate restored save: %w", errors.Join(err, rollbackErr))
	}
	if err := os.Remove(backup); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove preserved save after restore: %w", err)
	}
	return nil
}

func removeTemporary(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
