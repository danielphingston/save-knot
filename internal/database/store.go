package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/saveknot/saveknot/internal/core"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db}
	if err := store.initialize(ctx); err != nil {
		closeErr := db.Close()
		return nil, errors.Join(err, closeErr)
	}
	return store, nil
}

func (s *Store) initialize(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`CREATE TABLE IF NOT EXISTS games (
			id TEXT PRIMARY KEY,
			catalog_id TEXT NOT NULL DEFAULT '',
			catalog_name TEXT NOT NULL DEFAULT '',
			display_name TEXT NOT NULL,
			store TEXT NOT NULL,
			store_id TEXT NOT NULL DEFAULT '',
			install_path TEXT NOT NULL DEFAULT '',
			image TEXT NOT NULL DEFAULT '',
			notes TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			sync_enabled INTEGER NOT NULL DEFAULT 1,
			hidden INTEGER NOT NULL DEFAULT 0,
			last_seen INTEGER,
			last_change INTEGER,
			last_backup INTEGER
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS games_store_id ON games(store, store_id) WHERE store_id != ''`,
		`CREATE TABLE IF NOT EXISTS game_paths (
			id TEXT PRIMARY KEY,
			game_id TEXT NOT NULL REFERENCES games(id) ON DELETE CASCADE,
			source TEXT NOT NULL,
			template TEXT NOT NULL,
			resolved TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			UNIQUE(game_id, template, resolved)
		)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			id TEXT PRIMARY KEY,
			game_id TEXT NOT NULL REFERENCES games(id) ON DELETE CASCADE,
			device_id TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			file_count INTEGER NOT NULL,
			original_size INTEGER NOT NULL,
			stored_size INTEGER NOT NULL,
			remote_state TEXT NOT NULL,
			manifest BLOB NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS game_registry (
			id TEXT PRIMARY KEY,
			game_id TEXT NOT NULL REFERENCES games(id) ON DELETE CASCADE,
			source TEXT NOT NULL,
			path TEXT NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1,
			UNIQUE(game_id, path)
		)`,
		`CREATE TABLE IF NOT EXISTS game_exclusions (
			id TEXT PRIMARY KEY,
			game_id TEXT NOT NULL REFERENCES games(id) ON DELETE CASCADE,
			pattern TEXT NOT NULL,
			UNIQUE(game_id, pattern)
		)`,
		`CREATE TABLE IF NOT EXISTS game_policies (
			game_id TEXT PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
			quiet_seconds INTEGER NOT NULL,
			min_gap_seconds INTEGER NOT NULL,
			max_dirty_seconds INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS snapshots_game_created ON snapshots(game_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS snapshot_blobs (snapshot_id TEXT NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE, hash TEXT NOT NULL, PRIMARY KEY(snapshot_id, hash))`,
		`CREATE TABLE IF NOT EXISTS blobs (
			hash TEXT PRIMARY KEY,
			local_path TEXT NOT NULL,
			original_size INTEGER NOT NULL,
			stored_size INTEGER NOT NULL,
			uploaded_to TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS pending_blob_sources (
			source_path TEXT NOT NULL,
			target_root TEXT NOT NULL,
			hash TEXT NOT NULL,
			replacement_path TEXT NOT NULL,
			original_size INTEGER NOT NULL,
			PRIMARY KEY(source_path, target_root, hash)
		)`,
		`CREATE TABLE IF NOT EXISTS relocation_artifacts (
			artifact_id TEXT PRIMARY KEY,
			hash TEXT NOT NULL,
			source_path TEXT NOT NULL,
			target_root TEXT NOT NULL,
			destination_path TEXT NOT NULL,
			staging_path TEXT NOT NULL,
			original_size INTEGER NOT NULL,
			staging_owned INTEGER NOT NULL DEFAULT 0,
			finalized_by_rename INTEGER NOT NULL DEFAULT 0,
			state TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS diagnostic_cache (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			payload BLOB NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS activity_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			timestamp INTEGER NOT NULL,
			payload BLOB NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS activity_events_timestamp ON activity_events(timestamp)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize state database: %w", err)
		}
	}
	if err := s.ensureSyncEnabledColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureHiddenColumn(ctx); err != nil {
		return err
	}
	if err := s.backfillSnapshotBlobs(ctx); err != nil {
		return err
	}
	return nil
}

type snapshotBlobReference struct{ snapshotID, hash string }

func (s *Store) backfillSnapshotBlobs(ctx context.Context) (err error) {
	refs, err := s.snapshotBlobReferences(ctx)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin blob reference migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	for _, ref := range refs {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO snapshot_blobs (snapshot_id, hash) VALUES (?, ?)`, ref.snapshotID, ref.hash); err != nil {
			return fmt.Errorf("backfill blob reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit blob reference migration: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) snapshotBlobReferences(ctx context.Context) ([]snapshotBlobReference, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, manifest FROM snapshots`)
	if err != nil {
		return nil, fmt.Errorf("list snapshots for blob reference migration: %w", err)
	}
	var refs []snapshotBlobReference
	for rows.Next() {
		var id string
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, fmt.Errorf("scan snapshot for blob migration: %w", errors.Join(err, rows.Close()))
		}
		var snapshot core.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, fmt.Errorf("decode snapshot %q for blob migration: %w", id, errors.Join(err, rows.Close()))
		}
		for _, file := range snapshot.Files {
			if file.Hash != "" {
				refs = append(refs, snapshotBlobReference{id, file.Hash})
			}
		}
		if snapshot.Registry != nil && snapshot.Registry.Hash != "" {
			refs = append(refs, snapshotBlobReference{id, snapshot.Registry.Hash})
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("read snapshots for blob migration: %w", err)
	}
	return refs, nil
}

func (s *Store) SaveDiagnostics(ctx context.Context, diagnostics core.Diagnostics) error {
	updatedAt := time.Now().UTC()
	if diagnostics.UpdatedAt != nil {
		updatedAt = diagnostics.UpdatedAt.UTC()
	}
	payload, err := json.Marshal(diagnostics)
	if err != nil {
		return fmt.Errorf("encode diagnostics cache: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO diagnostic_cache (id, payload, updated_at) VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET payload = excluded.payload, updated_at = excluded.updated_at`, payload, updatedAt.UnixMilli()); err != nil {
		return fmt.Errorf("save diagnostics cache: %w", err)
	}
	return nil
}

func (s *Store) LoadDiagnostics(ctx context.Context) (core.Diagnostics, bool, error) {
	var payload []byte
	var updatedMillis int64
	err := s.db.QueryRowContext(ctx, `SELECT payload, updated_at FROM diagnostic_cache WHERE id = 1`).Scan(&payload, &updatedMillis)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Diagnostics{}, false, nil
	}
	if err != nil {
		return core.Diagnostics{}, false, fmt.Errorf("load diagnostics cache: %w", err)
	}
	var diagnostics core.Diagnostics
	if err := json.Unmarshal(payload, &diagnostics); err != nil {
		return core.Diagnostics{}, false, fmt.Errorf("decode diagnostics cache: %w", err)
	}
	updatedAt := time.UnixMilli(updatedMillis).UTC()
	diagnostics.UpdatedAt = &updatedAt
	return diagnostics, true, nil
}

func (s *Store) ensureSyncEnabledColumn(ctx context.Context) (err error) {
	return s.ensureGameColumn(ctx, "sync_enabled", `ALTER TABLE games ADD COLUMN sync_enabled INTEGER NOT NULL DEFAULT 1`)
}

func (s *Store) ensureHiddenColumn(ctx context.Context) error {
	return s.ensureGameColumn(ctx, "hidden", `ALTER TABLE games ADD COLUMN hidden INTEGER NOT NULL DEFAULT 0`)
}

func (s *Store) ensureGameColumn(ctx context.Context, target, statement string) (err error) {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(games)`)
	if err != nil {
		return fmt.Errorf("inspect games columns: %w", err)
	}
	found := false
	for rows.Next() {
		var sequence, notNull, primaryKey int
		var name, dataType string
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("scan games column: %w", errors.Join(err, rows.Close()))
		}
		if name == target {
			found = true
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("inspect games columns: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("add games.%s: %w", target, err)
	}
	return nil
}

func (s *Store) GamePolicy(ctx context.Context, gameID string) (core.BackupPolicy, error) {
	policy := core.DefaultBackupPolicy()
	err := s.db.QueryRowContext(ctx, `SELECT quiet_seconds, min_gap_seconds, max_dirty_seconds FROM game_policies WHERE game_id = ?`, gameID).
		Scan(&policy.QuietSeconds, &policy.MinGapSeconds, &policy.MaxDirtySeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return core.BackupPolicy{}, fmt.Errorf("load backup policy for %q: %w", gameID, err)
	}
	return policy, nil
}

func (s *Store) UpdateGamePolicy(ctx context.Context, gameID string, policy core.BackupPolicy) error {
	if policy.QuietSeconds < 1 || policy.MinGapSeconds < 0 || policy.MaxDirtySeconds < policy.QuietSeconds {
		return errors.New("backup policy requires quiet >= 1 second, min gap >= 0, and max dirty >= quiet")
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO game_policies (game_id, quiet_seconds, min_gap_seconds, max_dirty_seconds)
		VALUES (?, ?, ?, ?) ON CONFLICT(game_id) DO UPDATE SET quiet_seconds = excluded.quiet_seconds,
		min_gap_seconds = excluded.min_gap_seconds, max_dirty_seconds = excluded.max_dirty_seconds`,
		gameID, policy.QuietSeconds, policy.MinGapSeconds, policy.MaxDirtySeconds); err != nil {
		return fmt.Errorf("update backup policy for %q: %w", gameID, err)
	}
	return nil
}

func (s *Store) ListExclusions(ctx context.Context, gameID string) (exclusions []core.GameExclusion, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, game_id, pattern FROM game_exclusions WHERE game_id = ? ORDER BY pattern`, gameID)
	if err != nil {
		return nil, fmt.Errorf("list exclusions for %q: %w", gameID, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var exclusion core.GameExclusion
		if err := rows.Scan(&exclusion.ID, &exclusion.GameID, &exclusion.Pattern); err != nil {
			return nil, fmt.Errorf("scan game exclusion: %w", err)
		}
		exclusions = append(exclusions, exclusion)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate game exclusions: %w", err)
	}
	return exclusions, nil
}

func (s *Store) AddExclusion(ctx context.Context, exclusion core.GameExclusion) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO game_exclusions (id, game_id, pattern) VALUES (?, ?, ?)`, exclusion.ID, exclusion.GameID, exclusion.Pattern); err != nil {
		return fmt.Errorf("add game exclusion: %w", err)
	}
	return nil
}

func (s *Store) DeleteExclusion(ctx context.Context, gameID, exclusionID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM game_exclusions WHERE id = ? AND game_id = ?`, exclusionID, gameID)
	if err != nil {
		return fmt.Errorf("delete game exclusion %q: %w", exclusionID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted game exclusion %q: %w", exclusionID, err)
	}
	if count == 0 {
		return fmt.Errorf("game exclusion %q: %w", exclusionID, sql.ErrNoRows)
	}
	return nil
}

func (s *Store) ReplaceCatalogRegistry(ctx context.Context, gameID string, paths []core.RegistryPath) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin registry path update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_registry WHERE game_id = ? AND source = 'catalog'`, gameID); err != nil {
		return fmt.Errorf("clear catalog registry paths for %q: %w", gameID, err)
	}
	for _, registryPath := range paths {
		if _, err := tx.ExecContext(ctx, `INSERT INTO game_registry (id, game_id, source, path, enabled) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(game_id, path) DO NOTHING`, registryPath.ID, gameID, registryPath.Source, registryPath.Path, registryPath.Enabled); err != nil {
			return fmt.Errorf("add registry path for %q: %w", gameID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit registry path update: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) GameRegistry(ctx context.Context, gameID string) (paths []core.RegistryPath, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, game_id, source, path, enabled FROM game_registry WHERE game_id = ? ORDER BY path`, gameID)
	if err != nil {
		return nil, fmt.Errorf("list registry paths for %q: %w", gameID, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var registryPath core.RegistryPath
		if err := rows.Scan(&registryPath.ID, &registryPath.GameID, &registryPath.Source, &registryPath.Path, &registryPath.Enabled); err != nil {
			return nil, fmt.Errorf("scan registry path: %w", err)
		}
		paths = append(paths, registryPath)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate registry paths: %w", err)
	}
	return paths, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) UpsertGame(ctx context.Context, game core.Game) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO games (
		id, catalog_id, catalog_name, display_name, store, store_id, install_path, image, notes, enabled, sync_enabled, last_seen
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		catalog_id = excluded.catalog_id,
		catalog_name = excluded.catalog_name,
		store = excluded.store,
		store_id = excluded.store_id,
		install_path = excluded.install_path,
		last_seen = excluded.last_seen`,
		game.ID, game.CatalogID, game.CatalogName, game.DisplayName, game.Store, game.StoreID,
		game.InstallPath, game.Image, game.Notes, game.Enabled, game.SyncEnabled, timeValue(game.LastSeen),
	)
	if err != nil {
		return fmt.Errorf("upsert game %q: %w", game.ID, err)
	}
	return nil
}

// SaveGameRelations persists a game and its related paths/registry entries as
// one unit, preventing partial discovery, remap, or manual-create state.
func (s *Store) SaveGameRelations(ctx context.Context, game core.Game, paths []core.GamePath, registry []core.RegistryPath, replaceRegistry bool) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin game relation update: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err = tx.ExecContext(ctx, `INSERT INTO games (id,catalog_id,catalog_name,display_name,store,store_id,install_path,image,notes,enabled,sync_enabled,last_seen) VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET catalog_id=excluded.catalog_id,catalog_name=excluded.catalog_name,store=excluded.store,store_id=excluded.store_id,install_path=excluded.install_path,last_seen=excluded.last_seen`, game.ID, game.CatalogID, game.CatalogName, game.DisplayName, game.Store, game.StoreID, game.InstallPath, game.Image, game.Notes, game.Enabled, game.SyncEnabled, timeValue(game.LastSeen)); err != nil {
		return fmt.Errorf("upsert game %q: %w", game.ID, err)
	}
	if err = saveGamePaths(ctx, tx, game.ID, paths); err != nil {
		return err
	}
	if replaceRegistry {
		if _, err = tx.ExecContext(ctx, `DELETE FROM game_registry WHERE game_id=? AND source='catalog'`, game.ID); err != nil {
			return err
		}
		for _, item := range registry {
			if _, err = tx.ExecContext(ctx, `INSERT INTO game_registry (id,game_id,source,path,enabled) VALUES (?,?,?,?,?) ON CONFLICT(game_id,path) DO NOTHING`, item.ID, game.ID, item.Source, item.Path, item.Enabled); err != nil {
				return err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit game relation update: %w", err)
	}
	return nil
}

func saveGamePaths(ctx context.Context, tx *sql.Tx, gameID string, paths []core.GamePath) error {
	desired := make(map[string]struct{}, len(paths))
	for _, item := range paths {
		desired[item.ID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO game_paths (id,game_id,source,template,resolved,enabled) VALUES (?,?,?,?,?,?) ON CONFLICT(game_id,template,resolved) DO UPDATE SET id=excluded.id`, item.ID, gameID, item.Source, item.Template, filepath.Clean(item.Resolved), item.Enabled); err != nil {
			return fmt.Errorf("add path for %q: %w", gameID, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM game_paths WHERE game_id=? AND source='catalog'`, gameID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return errors.Join(err, rows.Close())
		}
		if _, ok := desired[id]; !ok {
			stale = append(stale, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM game_paths WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) CreateManualGame(ctx context.Context, game core.Game, paths []core.GamePath) error {
	return s.SaveGameRelations(ctx, game, paths, nil, false)
}

func (s *Store) EnsureRemoteGame(ctx context.Context, game core.Game) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO games (
		id, catalog_id, catalog_name, display_name, store, store_id, install_path, image, notes, enabled, sync_enabled, last_seen
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO NOTHING`,
		game.ID, game.CatalogID, game.CatalogName, game.DisplayName, game.Store, game.StoreID,
		game.InstallPath, game.Image, game.Notes, game.Enabled, game.SyncEnabled, timeValue(game.LastSeen),
	)
	if err != nil {
		return fmt.Errorf("ensure remote game %q: %w", game.ID, err)
	}
	return nil
}

func (s *Store) ReplaceCatalogPaths(ctx context.Context, gameID string, paths []core.GamePath) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin path update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	desired := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		desired[path.ID] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO game_paths (id, game_id, source, template, resolved, enabled)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(game_id, template, resolved) DO UPDATE SET id = excluded.id`,
			path.ID, gameID, path.Source, path.Template, filepath.Clean(path.Resolved), path.Enabled); err != nil {
			return fmt.Errorf("add path for %q: %w", gameID, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM game_paths WHERE game_id = ? AND source = 'catalog'`, gameID)
	if err != nil {
		return fmt.Errorf("list stale catalog paths for %q: %w", gameID, err)
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan stale catalog path: %w", errors.Join(err, rows.Close()))
		}
		if _, ok := desired[id]; !ok {
			stale = append(stale, id)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("list stale catalog paths: %w", err)
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM game_paths WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete stale catalog path %q: %w", id, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit path update: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) UpdatePath(ctx context.Context, gameID, pathID string, enabled bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE game_paths SET enabled = ? WHERE id = ? AND game_id = ?`, enabled, pathID, gameID)
	if err != nil {
		return fmt.Errorf("update save path %q: %w", pathID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check updated save path %q: %w", pathID, err)
	}
	if count == 0 {
		return fmt.Errorf("save path %q: %w", pathID, sql.ErrNoRows)
	}
	return nil
}

func (s *Store) DeletePath(ctx context.Context, gameID, pathID string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM game_paths WHERE id = ? AND game_id = ? AND source = 'custom'`, pathID, gameID)
	if err != nil {
		return fmt.Errorf("delete custom save path %q: %w", pathID, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted save path %q: %w", pathID, err)
	}
	if count == 0 {
		return errors.New("only custom save locations can be deleted; exclude catalog locations instead")
	}
	return nil
}

func (s *Store) AddPath(ctx context.Context, path core.GamePath) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO game_paths (id, game_id, source, template, resolved, enabled)
		VALUES (?, ?, ?, ?, ?, ?)`, path.ID, path.GameID, path.Source, path.Template, filepath.Clean(path.Resolved), path.Enabled)
	if err != nil {
		return fmt.Errorf("add game path: %w", err)
	}
	return nil
}

func (s *Store) ListGames(ctx context.Context) (games []core.Game, err error) {
	return s.listGames(ctx, false)
}

func (s *Store) ListHiddenGames(ctx context.Context) ([]core.Game, error) {
	return s.listGames(ctx, true)
}

func (s *Store) listGames(ctx context.Context, hidden bool) (games []core.Game, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
		g.id, g.catalog_id, g.catalog_name, g.display_name, g.store, g.store_id, g.install_path,
		g.image, g.notes, g.enabled, g.sync_enabled, g.hidden, g.last_seen, g.last_change, g.last_backup,
		COUNT(s.id), COALESCE(SUM(s.stored_size), 0),
		COALESCE(SUM(CASE WHEN s.remote_state != 'synced' THEN 1 ELSE 0 END), 0),
		(SELECT COUNT(*) FROM game_paths p WHERE p.game_id = g.id AND p.enabled = 1)
		+ (SELECT COUNT(*) FROM game_registry r WHERE r.game_id = g.id AND r.enabled = 1)
	FROM games g LEFT JOIN snapshots s ON s.game_id = g.id
	WHERE g.hidden = ? GROUP BY g.id ORDER BY g.display_name COLLATE NOCASE`, hidden)
	if err != nil {
		return nil, fmt.Errorf("list games: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		game, err := scanGame(rows)
		if err != nil {
			return nil, err
		}
		games = append(games, game)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate games: %w", err)
	}
	return games, nil
}

func (s *Store) Game(ctx context.Context, id string) (core.Game, error) {
	row := s.db.QueryRowContext(ctx, `SELECT
		g.id, g.catalog_id, g.catalog_name, g.display_name, g.store, g.store_id, g.install_path,
		g.image, g.notes, g.enabled, g.sync_enabled, g.hidden, g.last_seen, g.last_change, g.last_backup,
		COUNT(s.id), COALESCE(SUM(s.stored_size), 0),
		COALESCE(SUM(CASE WHEN s.remote_state != 'synced' THEN 1 ELSE 0 END), 0),
		(SELECT COUNT(*) FROM game_paths p WHERE p.game_id = g.id AND p.enabled = 1)
		+ (SELECT COUNT(*) FROM game_registry r WHERE r.game_id = g.id AND r.enabled = 1)
	FROM games g LEFT JOIN snapshots s ON s.game_id = g.id WHERE g.id = ? GROUP BY g.id`, id)
	game, err := scanGame(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Game{}, fmt.Errorf("%w: %q", core.ErrGameNotFound, id)
	}
	return game, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanGame(row scanner) (core.Game, error) {
	var game core.Game
	var enabled, syncEnabled, hidden bool
	var lastSeen, lastChange, lastBackup sql.NullInt64
	if err := row.Scan(
		&game.ID, &game.CatalogID, &game.CatalogName, &game.DisplayName, &game.Store, &game.StoreID,
		&game.InstallPath, &game.Image, &game.Notes, &enabled, &syncEnabled, &hidden, &lastSeen, &lastChange, &lastBackup,
		&game.SnapshotCount, &game.StoredSize, &game.PendingCount, &game.SourceCount,
	); err != nil {
		return core.Game{}, fmt.Errorf("scan game: %w", err)
	}
	game.Enabled = enabled
	game.SyncEnabled = syncEnabled
	game.Hidden = hidden
	game.LastSeen = timePointer(lastSeen)
	game.LastChange = timePointer(lastChange)
	game.LastBackup = timePointer(lastBackup)
	return game, nil
}

func (s *Store) SetGameHidden(ctx context.Context, id string, hidden bool) error {
	result, err := s.db.ExecContext(ctx, `UPDATE games SET hidden = ? WHERE id = ?`, hidden, id)
	if err != nil {
		return fmt.Errorf("update library state for game %q: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check library state for game %q: %w", id, err)
	}
	if count == 0 {
		return fmt.Errorf("%w: %q", core.ErrGameNotFound, id)
	}
	return nil
}

func (s *Store) GamePaths(ctx context.Context, gameID string) (paths []core.GamePath, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, game_id, source, template, resolved, enabled
		FROM game_paths WHERE game_id = ? ORDER BY source, resolved`, gameID)
	if err != nil {
		return nil, fmt.Errorf("list paths for %q: %w", gameID, err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var path core.GamePath
		if err := rows.Scan(&path.ID, &path.GameID, &path.Source, &path.Template, &path.Resolved, &path.Enabled); err != nil {
			return nil, fmt.Errorf("scan game path: %w", err)
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate game paths: %w", err)
	}
	return paths, nil
}

func (s *Store) UpdateGame(ctx context.Context, id string, update core.GameUpdate) error {
	game, err := s.Game(ctx, id)
	if err != nil {
		return err
	}
	if update.DisplayName != nil {
		game.DisplayName = *update.DisplayName
	}
	if update.Image != nil {
		game.Image = *update.Image
	}
	if update.Notes != nil {
		game.Notes = *update.Notes
	}
	if update.Enabled != nil {
		game.Enabled = *update.Enabled
	}
	if update.SyncEnabled != nil {
		game.SyncEnabled = *update.SyncEnabled
	}
	_, err = s.db.ExecContext(ctx, `UPDATE games SET display_name = ?, image = ?, notes = ?, enabled = ?, sync_enabled = ? WHERE id = ?`,
		game.DisplayName, game.Image, game.Notes, game.Enabled, game.SyncEnabled, id)
	if err != nil {
		return fmt.Errorf("update game %q: %w", id, err)
	}
	return nil
}

func (s *Store) SaveSnapshot(ctx context.Context, snapshot core.Snapshot) (err error) {
	manifest, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode snapshot manifest: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin snapshot transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshots
		(id, game_id, device_id, created_at, file_count, original_size, stored_size, remote_state, manifest)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, snapshot.ID, snapshot.GameID, snapshot.DeviceID,
		snapshot.CreatedAt.UnixMilli(), len(snapshot.Files), snapshot.OriginalSize, snapshot.StoredSize,
		snapshot.RemoteState, manifest)
	if err != nil {
		return fmt.Errorf("save snapshot %q: %w", snapshot.ID, err)
	}
	if err := insertSnapshotBlobRefs(ctx, tx, snapshot); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE games SET last_backup = ?, last_change = ? WHERE id = ?`, snapshot.CreatedAt.UnixMilli(), snapshot.CreatedAt.UnixMilli(), snapshot.GameID); err != nil {
		return fmt.Errorf("update last backup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) ImportSnapshot(ctx context.Context, snapshot core.Snapshot) (err error) {
	snapshot.RemoteState = "synced"
	manifest, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode imported snapshot: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin remote snapshot import: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	_, err = tx.ExecContext(ctx, `INSERT INTO snapshots
		(id, game_id, device_id, created_at, file_count, original_size, stored_size, remote_state, manifest)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'synced', ?)
		ON CONFLICT(id) DO UPDATE SET remote_state = 'synced', manifest = excluded.manifest`,
		snapshot.ID, snapshot.GameID, snapshot.DeviceID, snapshot.CreatedAt.UnixMilli(), len(snapshot.Files), snapshot.OriginalSize, snapshot.StoredSize, manifest)
	if err != nil {
		return fmt.Errorf("import remote snapshot %q: %w", snapshot.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM snapshot_blobs WHERE snapshot_id = ?`, snapshot.ID); err != nil {
		return err
	}
	if err := insertSnapshotBlobRefs(ctx, tx, snapshot); err != nil {
		return err
	}
	timestamp := snapshot.CreatedAt.UnixMilli()
	if _, err := tx.ExecContext(ctx, `UPDATE games SET last_backup = CASE WHEN last_backup IS NULL OR last_backup < ? THEN ? ELSE last_backup END, last_change = CASE WHEN last_change IS NULL OR last_change < ? THEN ? ELSE last_change END WHERE id = ?`, timestamp, timestamp, timestamp, timestamp, snapshot.GameID); err != nil {
		return fmt.Errorf("update remote game backup time %q: %w", snapshot.GameID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit remote snapshot import: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) ListSnapshots(ctx context.Context, gameID string) (snapshots []core.Snapshot, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT manifest FROM snapshots WHERE game_id = ? ORDER BY created_at DESC`, gameID)
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan snapshot: %w", err)
		}
		var snapshot core.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, fmt.Errorf("decode snapshot: %w", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *Store) SnapshotsBeyond(ctx context.Context, gameID string, keep int) (snapshots []core.Snapshot, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT manifest FROM snapshots WHERE game_id = ? ORDER BY created_at DESC LIMIT -1 OFFSET ?`, gameID, keep)
	if err != nil {
		return nil, fmt.Errorf("list snapshots beyond retention: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan retained snapshot: %w", err)
		}
		var snapshot core.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, fmt.Errorf("decode retained snapshot: %w", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate retained snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *Store) PendingSnapshots(ctx context.Context) (snapshots []core.Snapshot, err error) {
	return s.pendingSnapshots(ctx, "", false)
}

func (s *Store) PendingSnapshotsForGame(ctx context.Context, gameID string) ([]core.Snapshot, error) {
	return s.pendingSnapshots(ctx, gameID, true)
}

func (s *Store) PendingSnapshotsForWatchedGames(ctx context.Context) (snapshots []core.Snapshot, err error) {
	query := `SELECT s.manifest FROM snapshots s JOIN games g ON g.id = s.game_id
		WHERE s.remote_state != 'synced' AND g.enabled = 1 AND g.sync_enabled = 1 AND g.hidden = 0 ORDER BY s.created_at`
	return s.scanPendingSnapshots(ctx, query, nil)
}

func (s *Store) pendingSnapshots(ctx context.Context, gameID string, filterGame bool) (snapshots []core.Snapshot, err error) {
	query := `SELECT s.manifest FROM snapshots s JOIN games g ON g.id = s.game_id WHERE s.remote_state != 'synced'`
	arguments := []any{}
	if filterGame {
		query += ` AND s.game_id = ?`
		arguments = append(arguments, gameID)
	} else {
		query += ` AND g.sync_enabled = 1 AND g.hidden = 0`
	}
	query += ` ORDER BY s.created_at`
	return s.scanPendingSnapshots(ctx, query, arguments)
}

func (s *Store) scanPendingSnapshots(ctx context.Context, query string, arguments []any) (snapshots []core.Snapshot, err error) {
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list pending snapshots: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("scan pending snapshot: %w", err)
		}
		var snapshot core.Snapshot
		if err := json.Unmarshal(data, &snapshot); err != nil {
			return nil, fmt.Errorf("decode pending snapshot: %w", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending snapshots: %w", err)
	}
	return snapshots, nil
}

func (s *Store) DeleteSnapshot(ctx context.Context, id string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin snapshot delete: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	result, err := tx.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete snapshot %q: %w", id, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check deleted snapshot %q: %w", id, err)
	}
	if count == 0 {
		return fmt.Errorf("snapshot %q: %w", id, sql.ErrNoRows)
	}
	rows, err := tx.QueryContext(ctx, `SELECT b.hash, b.local_path FROM blobs b WHERE NOT EXISTS (SELECT 1 FROM snapshot_blobs r WHERE r.hash = b.hash)`)
	if err != nil {
		return fmt.Errorf("find orphaned blobs: %w", err)
	}
	type orphan struct{ hash, path string }
	var orphans []orphan
	for rows.Next() {
		var item orphan
		if err := rows.Scan(&item.hash, &item.path); err != nil {
			return errors.Join(fmt.Errorf("scan orphan blob: %w", err), rows.Close())
		}
		orphans = append(orphans, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("read orphaned blobs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot delete: %w", err)
	}
	committed = true
	var removeErr error
	for _, item := range orphans {
		if err := s.removeOrphanBlob(ctx, item.hash, item.path); err != nil {
			removeErr = errors.Join(removeErr, err)
		}
	}
	return removeErr
}

// removeOrphanBlob holds a SQLite write transaction while checking references
// and removing the file. That prevents another snapshot from acquiring a
// reference between the orphan check and physical removal. The blob row stays
// available for retries whenever removal fails.
func (s *Store) removeOrphanBlob(ctx context.Context, hash, path string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin orphan cleanup for blob %q: %w", hash, err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	result, err := tx.ExecContext(ctx, `UPDATE blobs SET local_path = local_path WHERE hash = ? AND NOT EXISTS (SELECT 1 FROM snapshot_blobs WHERE hash = ?)`, hash, hash)
	if err != nil {
		return fmt.Errorf("claim orphan blob %q: %w", hash, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check orphan blob %q: %w", hash, err)
	}
	if count == 0 {
		return nil
	}
	if path != "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove orphan blob %q: %w", hash, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM blobs WHERE hash = ? AND NOT EXISTS (SELECT 1 FROM snapshot_blobs WHERE hash = ?)`, hash, hash); err != nil {
		return fmt.Errorf("delete orphan blob row %q: %w", hash, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit orphan cleanup for blob %q: %w", hash, err)
	}
	committed = true
	return nil
}

func (s *Store) Snapshot(ctx context.Context, id string) (core.Snapshot, error) {
	snapshot, err := snapshotFromRow(s.db.QueryRowContext(ctx, `SELECT manifest FROM snapshots WHERE id = ?`, id))
	if err != nil {
		return core.Snapshot{}, fmt.Errorf("load snapshot %q: %w", id, err)
	}
	return snapshot, nil
}

func (s *Store) HasSnapshot(ctx context.Context, id string) (bool, error) {
	var present int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM snapshots WHERE id = ?`, id).Scan(&present)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check snapshot %q: %w", id, err)
	}
	return true, nil
}

func (s *Store) LatestSnapshot(ctx context.Context, gameID string) (core.Snapshot, error) {
	snapshot, err := snapshotFromRow(s.db.QueryRowContext(ctx, `SELECT manifest FROM snapshots WHERE game_id = ? ORDER BY created_at DESC LIMIT 1`, gameID))
	if err != nil {
		return core.Snapshot{}, fmt.Errorf("load latest snapshot for %q: %w", gameID, err)
	}
	return snapshot, nil
}

func snapshotFromRow(row scanner) (core.Snapshot, error) {
	var data []byte
	if err := row.Scan(&data); err != nil {
		return core.Snapshot{}, err
	}
	var snapshot core.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return core.Snapshot{}, fmt.Errorf("decode snapshot manifest: %w", err)
	}
	return snapshot, nil
}

func (s *Store) SaveBlob(ctx context.Context, hash, path string, originalSize, storedSize int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO blobs (hash, local_path, original_size, stored_size)
		VALUES (?, ?, ?, ?) ON CONFLICT(hash) DO UPDATE SET local_path = excluded.local_path, original_size = excluded.original_size, stored_size = excluded.stored_size`, hash, path, originalSize, storedSize)
	if err != nil {
		return fmt.Errorf("save blob %q: %w", hash, err)
	}
	return nil
}

func (s *Store) BlobPath(ctx context.Context, hash string) (string, error) {
	var path string
	if err := s.db.QueryRowContext(ctx, `SELECT local_path FROM blobs WHERE hash = ?`, hash).Scan(&path); err != nil {
		return "", fmt.Errorf("load blob %q: %w", hash, err)
	}
	return path, nil
}

func (s *Store) BlobUploaded(ctx context.Context, hash, target string) (bool, error) {
	var uploadedTo string
	if err := s.db.QueryRowContext(ctx, `SELECT uploaded_to FROM blobs WHERE hash = ?`, hash).Scan(&uploadedTo); err != nil {
		return false, fmt.Errorf("load upload state for blob %q: %w", hash, err)
	}
	return uploadedTo == target, nil
}

func (s *Store) MarkBlobUploaded(ctx context.Context, hash, target string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE blobs SET uploaded_to = ? WHERE hash = ?`, target, hash); err != nil {
		return fmt.Errorf("mark blob %q uploaded: %w", hash, err)
	}
	return nil
}

// InvalidateBlobUploads marks a target cache cold after successful reconnection.
func (s *Store) InvalidateBlobUploads(ctx context.Context, target string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE blobs SET uploaded_to = '' WHERE uploaded_to = ?`, target); err != nil {
		return fmt.Errorf("invalidate R2 upload cache for %q: %w", target, err)
	}
	return nil
}

// BlobRelocation records verified source and destination paths for a relocation.
type BlobRelocation struct {
	store            *Store
	items            []blobRelocationItem
	root             string
	pendingUndo      []pendingBlobSourceRow
	pendingUndoKeys  []pendingBlobSourceKey
	pendingUndoReady bool
}

type pendingBlobSourceRow struct {
	sourcePath, targetRoot, hash, replacementPath string
	originalSize                                  int64
}

type pendingBlobSourceKey struct{ sourcePath, targetRoot, hash string }

type blobRelocationItem struct {
	hash, oldPath, newPath  string
	originalSize            int64
	artifactID, stagingPath string
}

// PrepareBlobRelocation copies and verifies indexed blobs without changing the database.
func (s *Store) PrepareBlobRelocation(ctx context.Context, root string) (*BlobRelocation, error) {
	items, err := s.listBlobsForRelocation(ctx)
	if err != nil {
		return nil, err
	}
	receipt := &BlobRelocation{store: s, items: items, root: filepath.Clean(root)}
	for i := range receipt.items {
		if err := s.prepareRelocationItem(ctx, receipt.root, &receipt.items[i]); err != nil {
			return nil, errors.Join(err, receipt.cleanupDestinationsAfterFailure(ctx))
		}
	}
	return receipt, nil
}

func (s *Store) listBlobsForRelocation(ctx context.Context) ([]blobRelocationItem, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hash, local_path, original_size FROM blobs`)
	if err != nil {
		return nil, fmt.Errorf("list blobs for relocation: %w", err)
	}
	var items []blobRelocationItem
	for rows.Next() {
		var item blobRelocationItem
		if err := rows.Scan(&item.hash, &item.oldPath, &item.originalSize); err != nil {
			return nil, errors.Join(err, rows.Close())
		}
		if len(item.hash) < 2 {
			return nil, errors.Join(fmt.Errorf("invalid blob hash %q", item.hash), rows.Close())
		}
		item.oldPath = filepath.Clean(item.oldPath)
		items = append(items, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) prepareRelocationItem(ctx context.Context, root string, item *blobRelocationItem) error {
	item.newPath = filepath.Join(root, item.hash[:2], item.hash+".zst")
	if sameBlobPath(item.oldPath, item.newPath) {
		if err := verifyBlobFile(item.newPath, item.hash, item.originalSize); err != nil {
			return fmt.Errorf("indexed blob %q is invalid: %w", item.hash, err)
		}
		return nil
	}
	artifactID, err := core.NewID(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("record relocation artifact for blob %q: %w", item.hash, err)
	}
	item.artifactID = artifactID
	item.stagingPath = item.newPath + ".prepare-" + artifactID
	// The durable journal reserves this unique staging name before O_EXCL creates it.
	// Recovery deletes the final path only when it shares an inode with this stage.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO relocation_artifacts
		(artifact_id, hash, source_path, target_root, destination_path, staging_path, original_size, staging_owned, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 'preparing')`, item.artifactID, item.hash, item.oldPath, root,
		item.newPath, item.stagingPath, item.originalSize); err != nil {
		return fmt.Errorf("journal relocation artifact for blob %q: %w", item.hash, err)
	}
	if err := s.relocateBlobStaged(ctx, item.hash, item.oldPath, item.newPath, item.stagingPath, item.artifactID, item.originalSize); err != nil {
		return fmt.Errorf("prepare replacement for blob %q: %w", item.hash, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// Switch atomically sets all paths to either the destination or the source paths.
//
//nolint:gocognit // Keep the index and its cleanup journal in one transaction.
func (r *BlobRelocation) Switch(ctx context.Context, toDestination bool) (err error) {
	if r == nil || r.store == nil {
		return errors.New("invalid blob relocation receipt")
	}
	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin blob relocation switch: %w", err)
	}
	committed := false
	var undoRows []pendingBlobSourceRow
	var undoKeys []pendingBlobSourceKey
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	for _, item := range r.items {
		var rows []pendingBlobSourceRow
		var keys []pendingBlobSourceKey
		if toDestination && !r.pendingUndoReady {
			rows, keys, err = r.capturePendingSourceUndo(ctx, tx, item)
			if err != nil {
				return err
			}
		}
		if err := r.switchRelocationItem(ctx, tx, item, toDestination); err != nil {
			return err
		}
		undoRows = append(undoRows, rows...)
		undoKeys = append(undoKeys, keys...)
	}
	if !toDestination && r.pendingUndoReady {
		if err := r.restorePendingSources(ctx, tx); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit blob relocation switch: %w", err)
	}
	committed = true
	if toDestination && !r.pendingUndoReady {
		r.pendingUndo, r.pendingUndoKeys, r.pendingUndoReady = undoRows, undoKeys, true
	}
	if !toDestination && r.pendingUndoReady {
		r.pendingUndo, r.pendingUndoKeys, r.pendingUndoReady = nil, nil, false
	}
	return nil
}

func (r *BlobRelocation) switchRelocationItem(ctx context.Context, tx *sql.Tx, item blobRelocationItem, toDestination bool) error {
	target := item.oldPath
	if toDestination {
		target = item.newPath
	}
	if _, err := tx.ExecContext(ctx, `UPDATE blobs SET local_path = ? WHERE hash = ?`, target, item.hash); err != nil {
		return fmt.Errorf("switch blob %q path: %w", item.hash, err)
	}
	if item.oldPath == item.newPath {
		return nil
	}
	if toDestination {
		if err := r.updatePendingSource(ctx, tx, item, true); err != nil {
			return err
		}
	}
	if item.artifactID != "" {
		state := "preparing"
		if toDestination {
			state = "committed"
		}
		if _, err := tx.ExecContext(ctx, `UPDATE relocation_artifacts SET state = ? WHERE artifact_id = ?`, state, item.artifactID); err != nil {
			return fmt.Errorf("update relocation artifact for blob %q: %w", item.hash, err)
		}
	}
	return nil
}

//nolint:gocognit // Snapshot collisions and source rows together for exact compensation.
func (r *BlobRelocation) capturePendingSourceUndo(ctx context.Context, tx *sql.Tx, item blobRelocationItem) ([]pendingBlobSourceRow, []pendingBlobSourceKey, error) {
	rows, err := tx.QueryContext(ctx, `SELECT source_path, target_root, hash, replacement_path, original_size FROM pending_blob_sources WHERE hash = ?`, item.hash)
	if err != nil {
		return nil, nil, fmt.Errorf("read pending source chain for blob %q: %w", item.hash, err)
	}
	var all []pendingBlobSourceRow
	for rows.Next() {
		var row pendingBlobSourceRow
		if err := rows.Scan(&row.sourcePath, &row.targetRoot, &row.hash, &row.replacementPath, &row.originalSize); err != nil {
			return nil, nil, errors.Join(err, rows.Close())
		}
		all = append(all, row)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, nil, err
	}
	var moved, before []pendingBlobSourceRow
	for _, row := range all {
		if !sameBlobPath(row.sourcePath, item.oldPath) && sameBlobPath(row.replacementPath, item.oldPath) {
			moved = append(moved, row)
		}
	}
	for _, row := range all {
		for _, prior := range moved {
			if row.sourcePath == prior.sourcePath && sameBlobPath(row.targetRoot, r.root) {
				before = append(before, row)
				break
			}
		}
		if row.sourcePath == item.oldPath && sameBlobPath(row.targetRoot, r.root) {
			before = append(before, row)
		}
	}
	var keys []pendingBlobSourceKey
	for _, row := range moved {
		found := false
		for _, prior := range before {
			if prior.sourcePath == row.sourcePath && prior.targetRoot == row.targetRoot && prior.hash == row.hash {
				found = true
				break
			}
		}
		if !found {
			before = append(before, row)
		}
		keys = appendUniquePendingKey(keys, pendingBlobSourceKey{row.sourcePath, r.root, row.hash})
	}
	keys = appendUniquePendingKey(keys, pendingBlobSourceKey{item.oldPath, r.root, item.hash})
	return before, keys, nil
}

func appendUniquePendingKey(keys []pendingBlobSourceKey, key pendingBlobSourceKey) []pendingBlobSourceKey {
	for _, existing := range keys {
		if existing == key {
			return keys
		}
	}
	return append(keys, key)
}

func (r *BlobRelocation) restorePendingSources(ctx context.Context, tx *sql.Tx) error {
	for _, key := range r.pendingUndoKeys {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pending_blob_sources WHERE source_path = ? AND target_root = ? AND hash = ?`, key.sourcePath, key.targetRoot, key.hash); err != nil {
			return fmt.Errorf("remove pending source created by relocation for %q: %w", key.sourcePath, err)
		}
	}
	for _, row := range r.pendingUndo {
		if _, err := tx.ExecContext(ctx, `INSERT INTO pending_blob_sources (source_path, target_root, hash, replacement_path, original_size) VALUES (?, ?, ?, ?, ?)`, row.sourcePath, row.targetRoot, row.hash, row.replacementPath, row.originalSize); err != nil {
			return fmt.Errorf("restore pending source for %q: %w", row.sourcePath, err)
		}
	}
	return nil
}

//nolint:gocognit,nestif // Retarget old rows and insert the immediate row in the switch transaction.
func (r *BlobRelocation) updatePendingSource(ctx context.Context, tx *sql.Tx, item blobRelocationItem, toDestination bool) error {
	if toDestination {
		rows, err := tx.QueryContext(ctx, `SELECT source_path, target_root, hash, replacement_path, original_size FROM pending_blob_sources WHERE hash = ?`, item.hash)
		if err != nil {
			return fmt.Errorf("list pending sources for blob %q: %w", item.hash, err)
		}
		var all []pendingBlobSourceRow
		for rows.Next() {
			var row pendingBlobSourceRow
			if err := rows.Scan(&row.sourcePath, &row.targetRoot, &row.hash, &row.replacementPath, &row.originalSize); err != nil {
				return errors.Join(err, rows.Close())
			}
			all = append(all, row)
		}
		if err := errors.Join(rows.Err(), rows.Close()); err != nil {
			return err
		}
		movedSources := make(map[string]bool)
		for _, row := range all {
			if !sameBlobPath(row.sourcePath, item.oldPath) && sameBlobPath(row.replacementPath, item.oldPath) {
				movedSources[row.sourcePath] = true
			}
		}
		for source := range movedSources {
			for _, row := range all {
				if row.sourcePath == source && (sameBlobPath(row.replacementPath, item.oldPath) || sameBlobPath(row.targetRoot, r.root)) {
					if _, err := tx.ExecContext(ctx, `DELETE FROM pending_blob_sources WHERE source_path = ? AND target_root = ? AND hash = ?`, row.sourcePath, row.targetRoot, row.hash); err != nil {
						return fmt.Errorf("retarget older pending source for blob %q: %w", item.hash, err)
					}
				}
			}
			var originalSize int64
			for _, row := range all {
				if row.sourcePath == source && sameBlobPath(row.replacementPath, item.oldPath) {
					originalSize = row.originalSize
					break
				}
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO pending_blob_sources (source_path, target_root, hash, replacement_path, original_size) VALUES (?, ?, ?, ?, ?)
				ON CONFLICT(source_path, target_root, hash) DO UPDATE SET replacement_path = excluded.replacement_path, original_size = excluded.original_size`, source, r.root, item.hash, item.newPath, originalSize); err != nil {
				return fmt.Errorf("record retargeted pending source for blob %q: %w", item.hash, err)
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO pending_blob_sources (source_path, target_root, hash, replacement_path, original_size)
			VALUES (?, ?, ?, ?, ?) ON CONFLICT(source_path, target_root, hash) DO UPDATE SET
			replacement_path = excluded.replacement_path, original_size = excluded.original_size`,
			item.oldPath, r.root, item.hash, item.newPath, item.originalSize)
		if err != nil {
			return fmt.Errorf("record obsolete source for blob %q: %w", item.hash, err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_blob_sources WHERE source_path = ? AND target_root = ? AND hash = ?`, item.oldPath, r.root, item.hash); err != nil {
		return fmt.Errorf("cancel obsolete source cleanup for blob %q: %w", item.hash, err)
	}
	return nil
}

// CleanupSources removes obsolete source files after replacements are indexed and verified.
func (r *BlobRelocation) CleanupSources(ctx context.Context) error {
	return r.store.CleanupPendingBlobSources(ctx, r.root)
}

// CleanupPendingBlobSources retries source removals recorded by a committed relocation.
// When activeRoots is supplied, entries for a different configured root are left untouched.
func (s *Store) CleanupPendingBlobSources(ctx context.Context, activeRoots ...string) error {
	if len(activeRoots) > 1 {
		return errors.New("cleanup accepts at most one active blob root")
	}
	var result error
	result = errors.Join(result, s.cleanupRelocationArtifacts(ctx))
	activeRoot := ""
	if len(activeRoots) == 1 {
		activeRoot = filepath.Clean(activeRoots[0])
	}
	rows, err := s.db.QueryContext(ctx, `SELECT source_path, target_root, hash, replacement_path, original_size FROM pending_blob_sources`)
	if err != nil {
		return fmt.Errorf("list pending blob source cleanup: %w", err)
	}
	type pendingSource struct {
		sourcePath, targetRoot, hash, replacementPath string
		originalSize                                  int64
	}
	var pending []pendingSource
	for rows.Next() {
		var item pendingSource
		if err := rows.Scan(&item.sourcePath, &item.targetRoot, &item.hash, &item.replacementPath, &item.originalSize); err != nil {
			return errors.Join(fmt.Errorf("scan pending blob source cleanup: %w", err), rows.Close())
		}
		pending = append(pending, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("read pending blob source cleanup: %w", err)
	}
	for _, item := range pending {
		if activeRoot != "" && !sameBlobPath(activeRoot, item.targetRoot) {
			continue
		}
		if err := s.cleanupPendingBlobSource(ctx, item.sourcePath, item.targetRoot, item.hash, item.replacementPath, item.originalSize); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Store) cleanupPendingBlobSource(ctx context.Context, sourcePath, targetRoot, hash, replacementPath string, originalSize int64) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cleanup for obsolete blob %q: %w", sourcePath, err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	// Acquire SQLite's write lock before checking references so a concurrent path switch
	// cannot make the source live between the check and os.Remove.
	if _, err := tx.ExecContext(ctx, `UPDATE pending_blob_sources SET source_path = source_path
		WHERE source_path = ? AND target_root = ? AND hash = ?`, sourcePath, targetRoot, hash); err != nil {
		return fmt.Errorf("lock pending source %q for cleanup: %w", sourcePath, err)
	}
	var indexed string
	if err := tx.QueryRowContext(ctx, `SELECT local_path FROM blobs WHERE hash = ?`, hash).Scan(&indexed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("confirm replacement for obsolete blob %q: %w", sourcePath, err)
	}
	if !sameBlobPath(indexed, replacementPath) || !pathWithinRoot(indexed, targetRoot) {
		return nil
	}
	if err := verifyBlobFile(indexed, hash, originalSize); err != nil {
		return fmt.Errorf("verify indexed replacement for blob %q: %w", hash, err)
	}
	sharesReplacement, err := samePhysicalFile(sourcePath, indexed)
	if err != nil {
		return fmt.Errorf("compare obsolete source %q with replacement %q: %w", sourcePath, indexed, err)
	}
	if sharesReplacement {
		return s.finishPendingBlobSource(ctx, tx, sourcePath, targetRoot, hash, &committed)
	}
	referenced, err := s.hasIndexedBlobPathReference(ctx, tx, sourcePath)
	if err != nil {
		return err
	}
	if referenced {
		return nil
	}
	if err := os.Remove(sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove obsolete blob %q: %w", sourcePath, err)
	}
	return s.finishPendingBlobSource(ctx, tx, sourcePath, targetRoot, hash, &committed)
}

func (s *Store) hasIndexedBlobPathReference(ctx context.Context, tx *sql.Tx, sourcePath string) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT local_path FROM blobs`)
	if err != nil {
		return false, fmt.Errorf("list indexed blob paths while cleaning %q: %w", sourcePath, err)
	}
	var indexedPaths []string
	for rows.Next() {
		var indexedPath string
		if err := rows.Scan(&indexedPath); err != nil {
			return false, errors.Join(fmt.Errorf("scan indexed blob path for %q: %w", sourcePath, err), rows.Close())
		}
		indexedPaths = append(indexedPaths, indexedPath)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return false, fmt.Errorf("read indexed blob paths while cleaning %q: %w", sourcePath, err)
	}
	for _, indexedPath := range indexedPaths {
		if sameBlobPath(sourcePath, indexedPath) {
			return true, nil
		}
		sameFile, err := samePhysicalFile(sourcePath, indexedPath)
		if err != nil {
			return false, fmt.Errorf("compare obsolete source %q with indexed path %q: %w", sourcePath, indexedPath, err)
		}
		if sameFile {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) finishPendingBlobSource(ctx context.Context, tx *sql.Tx, sourcePath, targetRoot, hash string, committed *bool) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM pending_blob_sources WHERE source_path = ? AND target_root = ? AND hash = ?`, sourcePath, targetRoot, hash); err != nil {
		return fmt.Errorf("complete source cleanup for %q: %w", sourcePath, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source cleanup for %q: %w", sourcePath, err)
	}
	*committed = true
	return nil
}

// Stat follows symlinks and directory junctions; SameFile compares their resolved file identities.
func samePhysicalFile(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func sameBlobPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func pathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Rollback restores source paths before removing only destinations created by this attempt.
func (r *BlobRelocation) Rollback(ctx context.Context) error {
	if r == nil || r.store == nil {
		return errors.New("invalid blob relocation receipt")
	}
	compensationCtx, cancel := relocationCompensationContext(ctx)
	defer cancel()
	return errors.Join(r.Switch(compensationCtx, false), r.cleanupDestinations(compensationCtx))
}

func (r *BlobRelocation) cleanupDestinations(ctx context.Context) error {
	var result error
	for _, item := range r.items {
		if item.artifactID == "" {
			continue
		}
		if err := r.store.cleanupRelocationArtifact(ctx, item.artifactID); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (s *Store) cleanupRelocationArtifacts(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT artifact_id FROM relocation_artifacts`)
	if err != nil {
		return fmt.Errorf("list relocation artifacts: %w", err)
	}
	var artifactIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return errors.Join(fmt.Errorf("scan relocation artifact: %w", err), rows.Close())
		}
		artifactIDs = append(artifactIDs, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return fmt.Errorf("read relocation artifacts: %w", err)
	}
	var result error
	for _, id := range artifactIDs {
		if err := s.cleanupRelocationArtifact(ctx, id); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

type relocationArtifact struct {
	id, hash, sourcePath, destinationPath, stagingPath, state string
	originalSize                                              int64
	stagingOwned, finalizedByRename                           bool
}

func (s *Store) cleanupRelocationArtifact(ctx context.Context, artifactID string) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cleanup for relocation artifact %q: %w", artifactID, err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	if _, err := tx.ExecContext(ctx, `UPDATE relocation_artifacts SET artifact_id = artifact_id WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("lock relocation artifact %q: %w", artifactID, err)
	}
	artifact, err := readRelocationArtifact(ctx, tx, artifactID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read relocation artifact %q: %w", artifactID, err)
	}
	if artifact.stagingOwned {
		if err := s.removeRelocationArtifactFiles(ctx, tx, artifact); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relocation_artifacts WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("complete cleanup for relocation artifact %q: %w", artifactID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit cleanup for relocation artifact %q: %w", artifactID, err)
	}
	committed = true
	return nil
}

func readRelocationArtifact(ctx context.Context, tx *sql.Tx, artifactID string) (relocationArtifact, error) {
	var artifact relocationArtifact
	var stagingOwned, finalizedByRename int
	if err := tx.QueryRowContext(ctx, `SELECT artifact_id, hash, source_path, destination_path, staging_path, original_size, staging_owned, finalized_by_rename, state
		FROM relocation_artifacts WHERE artifact_id = ?`, artifactID).Scan(
		&artifact.id, &artifact.hash, &artifact.sourcePath, &artifact.destinationPath, &artifact.stagingPath,
		&artifact.originalSize, &stagingOwned, &finalizedByRename, &artifact.state); err != nil {
		return relocationArtifact{}, err
	}
	artifact.stagingOwned = stagingOwned != 0
	artifact.finalizedByRename = finalizedByRename != 0
	return artifact, nil
}

func (s *Store) removeRelocationArtifactFiles(ctx context.Context, tx *sql.Tx, artifact relocationArtifact) error {
	var indexedPath string
	indexErr := tx.QueryRowContext(ctx, `SELECT local_path FROM blobs WHERE hash = ?`, artifact.hash).Scan(&indexedPath)
	if indexErr != nil && !errors.Is(indexErr, sql.ErrNoRows) {
		return fmt.Errorf("confirm relocation artifact %q ownership: %w", artifact.id, indexErr)
	}
	abandoned := artifact.state == "preparing" && indexErr == nil && sameBlobPath(indexedPath, artifact.sourcePath)
	if abandoned {
		sourceSharesDestination, err := samePhysicalFile(artifact.sourcePath, artifact.destinationPath)
		if err != nil {
			return fmt.Errorf("compare indexed source %q with abandoned destination %q: %w", artifact.sourcePath, artifact.destinationPath, err)
		}
		ownedDestination := sameFile(artifact.stagingPath, artifact.destinationPath) || artifact.finalizedByRename && fileMissing(artifact.stagingPath) && verifyBlobFile(artifact.destinationPath, artifact.hash, artifact.originalSize) == nil
		if ownedDestination && !sourceSharesDestination {
			if err := os.Remove(artifact.destinationPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove abandoned relocation destination %q: %w", artifact.destinationPath, err)
			}
		}
	}
	if err := os.Remove(artifact.stagingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove relocation staging artifact %q: %w", artifact.stagingPath, err)
	}
	return nil
}

func sameFile(left, right string) bool {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false
	}
	rightInfo, err := os.Stat(right)
	return err == nil && os.SameFile(leftInfo, rightInfo)
}

func fileMissing(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func finalizeStagedBlob(stagingPath, destinationPath string, link func(string, string) error, rename func(string, string) error, beforeRename func() error) (bool, error) {
	if err := link(stagingPath, destinationPath); err == nil {
		return false, nil
	} else if errors.Is(err, os.ErrExist) {
		return false, err
	}
	if err := beforeRename(); err != nil {
		return false, fmt.Errorf("journal staged rename: %w", err)
	}
	if err := rename(stagingPath, destinationPath); err != nil {
		return true, err
	}
	return true, nil
}

// RelocateBlobs copies indexed data and updates paths only after all copies succeed.
func (s *Store) RelocateBlobs(ctx context.Context, root string) (err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT hash, local_path, original_size FROM blobs`)
	if err != nil {
		return fmt.Errorf("list blobs for relocation: %w", err)
	}
	type relocation struct {
		hash, oldPath, newPath string
		originalSize           int64
	}
	var items []relocation
	for rows.Next() {
		var hash, oldPath string
		var originalSize int64
		if err := rows.Scan(&hash, &oldPath, &originalSize); err != nil {
			return errors.Join(err, rows.Close())
		}
		if len(hash) < 2 {
			return errors.Join(fmt.Errorf("invalid blob hash %q", hash), rows.Close())
		}
		items = append(items, relocation{hash: hash, oldPath: oldPath, newPath: filepath.Join(root, hash[:2], hash+".zst"), originalSize: originalSize})
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, item := range items {
		if err := relocateBlob(ctx, item.hash, item.oldPath, item.newPath, item.originalSize); err != nil {
			return err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, tx.Rollback())
		}
	}()
	for _, item := range items {
		if _, err := tx.ExecContext(ctx, `UPDATE blobs SET local_path = ? WHERE hash = ?`, item.newPath, item.hash); err != nil {
			return err
		}
	}
	err = tx.Commit()
	committed = err == nil
	return err
}

func relocateBlob(ctx context.Context, hash, oldPath, newPath string, originalSize int64) error {
	if oldPath == newPath {
		if err := verifyBlobFile(newPath, hash, originalSize); err != nil {
			return fmt.Errorf("indexed blob %q is invalid: %w", hash, err)
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return err
	}
	if err := verifyBlobFile(newPath, hash, originalSize); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("destination blob %q is invalid: %w", hash, err)
	}
	if err := verifyBlobFile(oldPath, hash, originalSize); err != nil {
		return fmt.Errorf("source blob %q is invalid: %w", hash, err)
	}
	source, err := openBlobFile(oldPath)
	if err != nil {
		return fmt.Errorf("open source blob %q: %w", hash, err)
	}
	destination, err := createBlobFile(newPath)
	if err != nil {
		closeErr := source.Close()
		if errors.Is(err, os.ErrExist) && closeErr == nil {
			if verifyErr := verifyBlobFile(newPath, hash, originalSize); verifyErr != nil {
				return fmt.Errorf("concurrent destination blob %q is invalid: %w", hash, verifyErr)
			}
			return nil
		}
		return errors.Join(err, closeErr)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := errors.Join(source.Close(), destination.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		return errors.Join(err, removePartialBlob(newPath))
	}
	if err := verifyBlobFile(newPath, hash, originalSize); err != nil {
		return errors.Join(fmt.Errorf("copied blob %q failed verification: %w", hash, err), removePartialBlob(newPath))
	}
	return nil
}

func (s *Store) relocateBlobStaged(ctx context.Context, hash, oldPath, newPath, stagingPath, artifactID string, originalSize int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(newPath), 0o700); err != nil {
		return err
	}
	if err := verifyBlobFile(newPath, hash, originalSize); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("destination blob %q is invalid: %w", hash, err)
	}
	if _, err := os.Lstat(stagingPath); err == nil {
		return fmt.Errorf("relocation staging path already exists: %q", stagingPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect relocation staging path %q: %w", stagingPath, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE relocation_artifacts SET staging_owned = 1 WHERE artifact_id = ?`, artifactID); err != nil {
		return fmt.Errorf("reserve relocation staging path for blob %q: %w", hash, err)
	}
	if err := verifyBlobFile(oldPath, hash, originalSize); err != nil {
		return fmt.Errorf("source blob %q is invalid: %w", hash, err)
	}
	source, err := openBlobFile(oldPath)
	if err != nil {
		return fmt.Errorf("open source blob %q: %w", hash, err)
	}
	destination, err := createBlobFile(stagingPath)
	if err != nil {
		_, ownershipErr := s.db.ExecContext(ctx, `UPDATE relocation_artifacts SET staging_owned = 0 WHERE artifact_id = ?`, artifactID)
		return errors.Join(err, source.Close(), ownershipErr)
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := errors.Join(source.Close(), destination.Close())
	if err := errors.Join(copyErr, closeErr); err != nil {
		return errors.Join(err, removePartialBlob(stagingPath))
	}
	if err := verifyBlobFile(stagingPath, hash, originalSize); err != nil {
		return errors.Join(fmt.Errorf("staged blob %q failed verification: %w", hash, err), removePartialBlob(stagingPath))
	}
	if err := ctx.Err(); err != nil {
		return errors.Join(err, removePartialBlob(stagingPath))
	}
	_, err = finalizeStagedBlob(stagingPath, newPath, os.Link, renameNoReplace, func() error {
		_, err := s.db.ExecContext(ctx, `UPDATE relocation_artifacts SET finalized_by_rename = 1 WHERE artifact_id = ?`, artifactID)
		return err
	})
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			if verifyErr := verifyBlobFile(newPath, hash, originalSize); verifyErr == nil {
				return nil
			} else {
				return fmt.Errorf("concurrent destination blob %q is invalid: %w", hash, verifyErr)
			}
		}
		return fmt.Errorf("finalize staged blob %q: %w", hash, err)
	}
	return nil
}

// openBlobFile anchors the final path component to its containing directory.
// os.Root prevents the blob filename from escaping through traversal or symlinks.
func openBlobFile(path string) (*os.File, error) {
	cleanPath := filepath.Clean(path)
	name := filepath.Base(cleanPath)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid blob file path %q", path)
	}
	root, err := os.OpenRoot(filepath.Dir(cleanPath))
	if err != nil {
		return nil, err
	}
	file, openErr := root.Open(name)
	rootErr := root.Close()
	if openErr != nil {
		return nil, errors.Join(openErr, rootErr)
	}
	if rootErr != nil {
		return nil, errors.Join(rootErr, file.Close())
	}
	return file, nil
}

func createBlobFile(path string) (*os.File, error) {
	cleanPath := filepath.Clean(path)
	name := filepath.Base(cleanPath)
	if name == "." || name == ".." || name == string(filepath.Separator) {
		return nil, fmt.Errorf("invalid blob file path %q", path)
	}
	root, err := os.OpenRoot(filepath.Dir(cleanPath))
	if err != nil {
		return nil, err
	}
	file, openErr := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	rootErr := root.Close()
	if openErr != nil {
		return nil, errors.Join(openErr, rootErr)
	}
	if rootErr != nil {
		return nil, errors.Join(rootErr, file.Close())
	}
	return file, nil
}

func removePartialBlob(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove incomplete blob %q: %w", path, err)
	}
	return nil
}

func verifyBlobFile(path, expectedHash string, expectedSize int64) error {
	if expectedSize < 0 || expectedSize == math.MaxInt64 {
		return errors.New("blob has an invalid original size")
	}
	decoded, err := hex.DecodeString(expectedHash)
	if err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid blob hash %q", expectedHash)
	}
	file, err := openBlobFile(path)
	if err != nil {
		return err
	}
	decoder, err := zstd.NewReader(file)
	if err != nil {
		return errors.Join(fmt.Errorf("decode blob %q: %w", path, err), file.Close())
	}
	digest := sha256.New()
	written, decodeErr := io.Copy(digest, io.LimitReader(decoder, expectedSize+1))
	decoder.Close()
	if err := errors.Join(decodeErr, file.Close()); err != nil {
		return fmt.Errorf("read blob %q: %w", path, err)
	}
	if written != expectedSize {
		return fmt.Errorf("blob %q decompressed size does not match", path)
	}
	if !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), expectedHash) {
		return fmt.Errorf("blob %q content hash does not match", path)
	}
	return nil
}

func (s *Store) MarkSnapshotRemote(ctx context.Context, id, state string) error {
	snapshot, err := s.Snapshot(ctx, id)
	if err != nil {
		return err
	}
	snapshot.RemoteState = state
	manifest, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode updated snapshot: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE snapshots SET remote_state = ?, manifest = ? WHERE id = ?`, state, manifest, id); err != nil {
		return fmt.Errorf("update remote state: %w", err)
	}
	return nil
}

func insertSnapshotBlobRefs(ctx context.Context, tx *sql.Tx, snapshot core.Snapshot) error {
	seen := make(map[string]struct{}, len(snapshot.Files)+1)
	insert := func(hash string) error {
		if hash == "" {
			return nil
		}
		if _, ok := seen[hash]; ok {
			return nil
		}
		seen[hash] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO snapshot_blobs (snapshot_id, hash) VALUES (?, ?)`, snapshot.ID, hash); err != nil {
			return fmt.Errorf("record snapshot blob reference %q: %w", hash, err)
		}
		return nil
	}
	for _, file := range snapshot.Files {
		if err := insert(file.Hash); err != nil {
			return err
		}
	}
	if snapshot.Registry != nil {
		if err := insert(snapshot.Registry.Hash); err != nil {
			return err
		}
	}
	return nil
}

func timeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

func timePointer(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	timestamp := time.UnixMilli(value.Int64).UTC()
	return &timestamp
}
