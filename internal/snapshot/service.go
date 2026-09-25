package snapshot

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/klauspost/compress/zstd"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/registrybackup"
)

var (
	ErrNoFiles     = errors.New("no save files found")
	ErrUnchanged   = errors.New("save data is unchanged since the latest snapshot")
	errFileChanged = errors.New("save file changed while it was being read")
)

type repository interface {
	GamePaths(context.Context, string) ([]core.GamePath, error)
	SaveBlob(context.Context, string, string, int64, int64) error
	BlobPath(context.Context, string) (string, error)
	SaveSnapshot(context.Context, core.Snapshot) error
	Snapshot(context.Context, string) (core.Snapshot, error)
}

type sourceRepository interface {
	GameRegistry(context.Context, string) ([]core.RegistryPath, error)
	ListExclusions(context.Context, string) ([]core.GameExclusion, error)
	LatestSnapshot(context.Context, string) (core.Snapshot, error)
}

type Service struct {
	repository repository
	sources    sourceRepository
	blobRootMu sync.RWMutex
	blobRoot   string
	deviceID   string
	now        func() time.Time
}

func (s *Service) SetBlobRoot(root string) {
	s.blobRootMu.Lock()
	s.blobRoot = root
	s.blobRootMu.Unlock()
}

func (s *Service) currentBlobRoot() string {
	s.blobRootMu.RLock()
	defer s.blobRootMu.RUnlock()
	return s.blobRoot
}

func (s *Service) BlobRoot() string {
	return s.currentBlobRoot()
}

func New(repository repository, sources sourceRepository, blobRoot, deviceID string) *Service {
	return &Service{repository: repository, sources: sources, blobRoot: blobRoot, deviceID: deviceID, now: time.Now}
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
		Metadata: core.PortableGameMetadata{DisplayName: game.DisplayName, Notes: game.Notes, Image: portableImage(game.Image)},
	}
	storedBlobs := make(map[string]int64)
	exclusions, err := s.sources.ListExclusions(ctx, game.ID)
	if err != nil {
		return core.Snapshot{}, err
	}
	for _, path := range paths {
		if !path.Enabled {
			continue
		}
		if err := s.addPath(ctx, path, exclusions, &snapshot, storedBlobs); err != nil {
			return core.Snapshot{}, err
		}
	}
	if err := s.addRegistry(ctx, game.ID, &snapshot, storedBlobs); err != nil {
		return core.Snapshot{}, err
	}
	if len(snapshot.Files) == 0 && snapshot.Registry == nil {
		return core.Snapshot{}, ErrNoFiles
	}
	for _, size := range storedBlobs {
		snapshot.StoredSize += size
	}
	latest, err := s.sources.LatestSnapshot(ctx, game.ID)
	if err == nil && sameContents(latest, snapshot) {
		return core.Snapshot{}, ErrUnchanged
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return core.Snapshot{}, err
	}
	if err := s.repository.SaveSnapshot(ctx, snapshot); err != nil {
		return core.Snapshot{}, err
	}
	return snapshot, nil
}

func portableImage(image string) string {
	if strings.HasPrefix(image, "https://") {
		return image
	}
	return ""
}

func sameContents(left, right core.Snapshot) bool {
	if left.Metadata != right.Metadata {
		return false
	}
	if len(left.Files) != len(right.Files) || (left.Registry == nil) != (right.Registry == nil) {
		return false
	}
	files := make(map[string]string, len(left.Files))
	for _, file := range left.Files {
		files[file.SourceKey+"\x00"+file.Path] = file.Hash
	}
	for _, file := range right.Files {
		if files[file.SourceKey+"\x00"+file.Path] != file.Hash {
			return false
		}
	}
	return left.Registry == nil || left.Registry.Hash == right.Registry.Hash
}

func (s *Service) addRegistry(ctx context.Context, gameID string, snapshot *core.Snapshot, storedBlobs map[string]int64) error {
	rules, err := s.sources.GameRegistry(ctx, gameID)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.Enabled {
			keys = append(keys, rule.Path)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	data, err := (registrybackup.Service{}).Backup(ctx, keys)
	if errors.Is(err, registrybackup.ErrUnsupported) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("back up Windows registry: %w", err)
	}
	stored, err := s.storeData(data)
	if err != nil {
		return err
	}
	if err := s.repository.SaveBlob(ctx, stored.hash, stored.path, stored.originalSize, stored.storedSize); err != nil {
		return err
	}
	snapshot.Registry = &core.SnapshotRegistry{Keys: keys, Hash: stored.hash, Size: stored.originalSize}
	snapshot.OriginalSize += stored.originalSize
	storedBlobs[stored.hash] = stored.storedSize
	return nil
}

func (s *Service) storeData(data []byte) (blob, error) {
	staging := filepath.Join(s.currentBlobRoot(), ".staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		return blob{}, fmt.Errorf("create registry staging directory: %w", err)
	}
	temporary, err := os.CreateTemp(staging, "registry-*.json")
	if err != nil {
		return blob{}, fmt.Errorf("create registry staging file: %w", err)
	}
	path := temporary.Name()
	if _, err := temporary.Write(data); err != nil {
		return blob{}, errors.Join(fmt.Errorf("write registry staging file: %w", err), temporary.Close(), removeTemporary(path))
	}
	if err := temporary.Close(); err != nil {
		return blob{}, errors.Join(fmt.Errorf("close registry staging file: %w", err), removeTemporary(path))
	}
	stored, storeErr := s.storeFile(path)
	return stored, errors.Join(storeErr, removeTemporary(path))
}

func (s *Service) addPath(ctx context.Context, path core.GamePath, exclusions []core.GameExclusion, snapshot *core.Snapshot, storedBlobs map[string]int64) error {
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
		if excluded(file, exclusions) {
			continue
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

func excluded(file scannedFile, exclusions []core.GameExclusion) bool {
	relative := filepath.ToSlash(file.relative)
	absolute := filepath.ToSlash(file.absolute)
	for _, exclusion := range exclusions {
		pattern := filepath.ToSlash(exclusion.Pattern)
		if matched, err := doublestar.Match(pattern, relative); err == nil && matched {
			return true
		}
		if matched, err := doublestar.Match(pattern, absolute); err == nil && matched {
			return true
		}
	}
	return false
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
	blobRoot := s.currentBlobRoot()
	before, err := os.Stat(path)
	if err != nil {
		return blob{}, err
	}
	if err := os.MkdirAll(filepath.Join(blobRoot, ".staging"), 0o700); err != nil {
		return blob{}, fmt.Errorf("create blob staging directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Join(blobRoot, ".staging"), "blob-*.zst")
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
	directory := filepath.Join(blobRoot, hashValue[:2])
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

func (s *Service) Restore(ctx context.Context, game core.Game, snapshotID string) (result core.Snapshot, resultErr error) {
	target, err := s.repository.Snapshot(ctx, snapshotID)
	if err != nil {
		return core.Snapshot{}, err
	}
	if target.GameID != game.ID {
		return core.Snapshot{}, errors.New("snapshot does not belong to this game")
	}
	preRestore, rollbackSnapshot, err := s.createRollbackSnapshot(ctx, game)
	if err != nil {
		return core.Snapshot{}, err
	}
	staged, err := s.stageRestoreFiles(ctx, game.ID, target)
	if err != nil {
		return core.Snapshot{}, err
	}
	defer func() {
		if cleanupErr := cleanupStagedRestoreFiles(staged); cleanupErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("remove staged restore files: %w", cleanupErr))
		}
	}()
	registryData, err := s.loadRestoreRegistry(ctx, target.Registry)
	if err != nil {
		return core.Snapshot{}, err
	}
	backups, err := s.activateAndRestore(ctx, target.Registry != nil, registryData, rollbackSnapshot, staged)
	if err != nil {
		return core.Snapshot{}, err
	}
	if err := removeRestoreBackups(backups); err != nil {
		return preRestore, fmt.Errorf("remove preserved saves after restore: %w", err)
	}
	return preRestore, nil
}

func (s *Service) createRollbackSnapshot(ctx context.Context, game core.Game) (core.Snapshot, core.Snapshot, error) {
	preRestore, err := s.Create(ctx, game)
	if err != nil && !errors.Is(err, ErrNoFiles) && !errors.Is(err, ErrUnchanged) {
		return core.Snapshot{}, core.Snapshot{}, fmt.Errorf("create pre-restore snapshot: %w", err)
	}
	rollbackSnapshot := preRestore
	if errors.Is(err, ErrUnchanged) {
		latest, latestErr := s.sources.LatestSnapshot(ctx, game.ID)
		if latestErr == nil {
			rollbackSnapshot = latest
		} else if !errors.Is(latestErr, sql.ErrNoRows) {
			return core.Snapshot{}, core.Snapshot{}, fmt.Errorf("load pre-restore snapshot for rollback: %w", latestErr)
		}
	}
	return preRestore, rollbackSnapshot, nil
}

func (s *Service) stageRestoreFiles(ctx context.Context, gameID string, target core.Snapshot) (staged []stagedRestoreFile, resultErr error) {
	paths, err := s.repository.GamePaths(ctx, gameID)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]core.GamePath)
	for _, path := range paths {
		if path.Enabled {
			sources[sourceKey(path)] = path
		}
	}
	defer func() {
		if resultErr != nil {
			if cleanupErr := cleanupStagedRestoreFiles(staged); cleanupErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove staged restore files: %w", cleanupErr))
			}
		}
	}()
	for _, file := range target.Files {
		if err := ctx.Err(); err != nil {
			return staged, err
		}
		source, ok := sources[file.SourceKey]
		if !ok {
			return staged, fmt.Errorf("save location for %q is not configured on this device", file.Path)
		}
		destination, err := restoreDestination(source, file)
		if err != nil {
			return staged, err
		}
		item, err := s.stageRestoreFile(ctx, destination, file)
		if err != nil {
			return staged, err
		}
		staged = append(staged, item)
	}
	return staged, nil
}

func cleanupStagedRestoreFiles(staged []stagedRestoreFile) error {
	var result error
	for _, item := range staged {
		result = errors.Join(result, removeTemporary(item.temporary))
	}
	return result
}

type stagedRestoreFile struct {
	destination string
	temporary   string
}

type restoreBackup struct {
	activated   bool
	destination string
	backup      string
}

func (s *Service) loadRestoreRegistry(ctx context.Context, registrySnapshot *core.SnapshotRegistry) ([]byte, error) {
	if registrySnapshot == nil {
		return nil, nil
	}
	blobPath, err := s.repository.BlobPath(ctx, registrySnapshot.Hash)
	if err != nil {
		return nil, err
	}
	return readCompressedBlob(blobPath, registrySnapshot.Hash, registrySnapshot.Size)
}

func (s *Service) activateAndRestore(ctx context.Context, hasRegistry bool, registryData []byte, rollbackSnapshot core.Snapshot, staged []stagedRestoreFile) ([]restoreBackup, error) {
	backups, err := activateRestoredFiles(staged)
	if err != nil {
		return nil, err
	}
	if !hasRegistry {
		return backups, nil
	}
	if err := (registrybackup.Service{}).Restore(ctx, registryData); err != nil {
		rollbackRegistryErr := s.restorePreviousRegistry(context.WithoutCancel(ctx), rollbackSnapshot)
		return nil, errors.Join(fmt.Errorf("restore Windows registry: %w", err), rollbackRegistryErr, rollbackRestoredFiles(backups))
	}
	return backups, nil
}

func removeRestoreBackups(backups []restoreBackup) error {
	var result error
	for _, item := range backups {
		if item.backup != "" {
			result = errors.Join(result, removeTemporary(item.backup))
		}
	}
	return result
}

func activateRestoredFiles(staged []stagedRestoreFile) ([]restoreBackup, error) {
	backups := make([]restoreBackup, 0, len(staged))
	rollback := func() error { return rollbackRestoredFiles(backups) }
	for _, item := range staged {
		backupID, err := core.NewID(time.Now().UTC())
		if err != nil {
			return nil, errors.Join(err, rollback())
		}
		backup := item.destination + ".saveknot-old-" + backupID
		if _, err := os.Stat(item.destination); err == nil {
			if err := os.Rename(item.destination, backup); err != nil {
				return nil, errors.Join(fmt.Errorf("preserve current save: %w", err), rollback())
			}
			backups = append(backups, restoreBackup{destination: item.destination, backup: backup})
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, errors.Join(fmt.Errorf("inspect restore destination: %w", err), rollback())
		} else {
			backups = append(backups, restoreBackup{destination: item.destination})
		}
		if err := os.Rename(item.temporary, item.destination); err != nil {
			return nil, errors.Join(fmt.Errorf("activate restored save: %w", err), rollback())
		}
		backups[len(backups)-1].activated = true
	}
	return backups, nil
}

func rollbackRestoredFiles(backups []restoreBackup) error {
	var result error
	for index := len(backups) - 1; index >= 0; index-- {
		item := backups[index]
		if item.activated {
			if err := os.Remove(item.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
		if item.backup != "" {
			if err := os.Rename(item.backup, item.destination); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}
	return result
}

func (s *Service) restorePreviousRegistry(ctx context.Context, snapshot core.Snapshot) error {
	if snapshot.Registry == nil {
		return nil
	}
	blobPath, err := s.repository.BlobPath(ctx, snapshot.Registry.Hash)
	if err != nil {
		return fmt.Errorf("load pre-restore registry blob: %w", err)
	}
	data, err := readCompressedBlob(blobPath, snapshot.Registry.Hash, snapshot.Registry.Size)
	if err != nil {
		return fmt.Errorf("verify pre-restore registry blob: %w", err)
	}
	if err := (registrybackup.Service{}).Restore(ctx, data); err != nil {
		return fmt.Errorf("roll back Windows registry: %w", err)
	}
	return nil
}

func readCompressedBlob(path, expectedHash string, size int64) ([]byte, error) {
	//nolint:gosec // The path is retrieved from SaveKnot's private blob index.
	input, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	decoder, err := zstd.NewReader(input)
	if err != nil {
		return nil, errors.Join(err, input.Close())
	}
	data, readErr := io.ReadAll(io.LimitReader(decoder, size+1))
	decoder.Close()
	if err := errors.Join(readErr, input.Close()); err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, errors.New("registry blob size did not match the snapshot")
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), expectedHash) {
		return nil, errors.New("registry blob failed SHA-256 verification")
	}
	return data, nil
}

func (s *Service) stageRestoreFile(ctx context.Context, destination string, file core.SnapshotFile) (stagedRestoreFile, error) {
	blobPath, err := s.repository.BlobPath(ctx, file.Hash)
	if err != nil {
		return stagedRestoreFile{}, err
	}
	//nolint:gosec // blobPath is retrieved from SaveKnot's private blob index.
	input, err := os.Open(blobPath)
	if err != nil {
		return stagedRestoreFile{}, fmt.Errorf("open blob %q: %w", file.Hash, err)
	}
	decoder, err := zstd.NewReader(input)
	if err != nil {
		return stagedRestoreFile{}, fmt.Errorf("decode blob %q: %w", file.Hash, errors.Join(err, input.Close()))
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		decoder.Close()
		return stagedRestoreFile{}, fmt.Errorf("create restore directory: %w", errors.Join(err, input.Close()))
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".saveknot-restore-*")
	if err != nil {
		decoder.Close()
		return stagedRestoreFile{}, fmt.Errorf("create restore file: %w", errors.Join(err, input.Close()))
	}
	temporaryPath := temporary.Name()
	digest := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(decoder, file.Size+1))
	decoder.Close()
	closeErr := errors.Join(input.Close(), temporary.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		return stagedRestoreFile{}, fmt.Errorf("write restored file: %w", errors.Join(err, removeTemporary(temporaryPath)))
	}
	if written != file.Size {
		return stagedRestoreFile{}, errors.Join(errors.New("restored blob size did not match the snapshot"), removeTemporary(temporaryPath))
	}
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), file.Hash) {
		return stagedRestoreFile{}, errors.Join(errors.New("restored blob failed hash verification"), removeTemporary(temporaryPath))
	}
	if err := os.Chmod(temporaryPath, fs.FileMode(file.Mode)); err != nil {
		return stagedRestoreFile{}, fmt.Errorf("restore file permissions: %w", errors.Join(err, removeTemporary(temporaryPath)))
	}
	if err := os.Chtimes(temporaryPath, file.Modified, file.Modified); err != nil {
		return stagedRestoreFile{}, fmt.Errorf("restore file timestamp: %w", errors.Join(err, removeTemporary(temporaryPath)))
	}
	return stagedRestoreFile{destination: destination, temporary: temporaryPath}, nil
}

func restoreDestination(source core.GamePath, file core.SnapshotFile) (string, error) {
	if file.RootFile {
		return source.Resolved, nil
	}
	relative := filepath.Clean(filepath.FromSlash(file.Path))
	if relative == "." || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("snapshot file path %q escapes its configured save location", file.Path)
	}
	destination := filepath.Join(source.Resolved, relative)
	contained, err := filepath.Rel(source.Resolved, destination)
	if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("snapshot file path %q escapes its configured save location", file.Path)
	}
	return destination, nil
}

func removeTemporary(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
