package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"time"

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
		`CREATE INDEX IF NOT EXISTS snapshots_game_created ON snapshots(game_id, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS blobs (
			hash TEXT PRIMARY KEY,
			local_path TEXT NOT NULL,
			original_size INTEGER NOT NULL,
			stored_size INTEGER NOT NULL,
			uploaded_to TEXT NOT NULL DEFAULT ''
		)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize state database: %w", err)
		}
	}
	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) UpsertGame(ctx context.Context, game core.Game) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO games (
		id, catalog_id, catalog_name, display_name, store, store_id, install_path, image, notes, enabled, last_seen
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
		catalog_id = excluded.catalog_id,
		catalog_name = excluded.catalog_name,
		store = excluded.store,
		store_id = excluded.store_id,
		install_path = excluded.install_path,
		last_seen = excluded.last_seen`,
		game.ID, game.CatalogID, game.CatalogName, game.DisplayName, game.Store, game.StoreID,
		game.InstallPath, game.Image, game.Notes, game.Enabled, timeValue(game.LastSeen),
	)
	if err != nil {
		return fmt.Errorf("upsert game %q: %w", game.ID, err)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM game_paths WHERE game_id = ? AND source = 'catalog'`, gameID); err != nil {
		return fmt.Errorf("clear catalog paths for %q: %w", gameID, err)
	}
	for _, path := range paths {
		if _, err := tx.ExecContext(ctx, `INSERT INTO game_paths (id, game_id, source, template, resolved, enabled)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(game_id, template, resolved) DO NOTHING`,
			path.ID, gameID, path.Source, path.Template, filepath.Clean(path.Resolved), path.Enabled); err != nil {
			return fmt.Errorf("add path for %q: %w", gameID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit path update: %w", err)
	}
	committed = true
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
	rows, err := s.db.QueryContext(ctx, `SELECT
		g.id, g.catalog_id, g.catalog_name, g.display_name, g.store, g.store_id, g.install_path,
		g.image, g.notes, g.enabled, g.last_seen, g.last_change, g.last_backup,
		COUNT(s.id), COALESCE(SUM(s.stored_size), 0)
	FROM games g LEFT JOIN snapshots s ON s.game_id = g.id
	GROUP BY g.id ORDER BY g.display_name COLLATE NOCASE`)
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
		g.image, g.notes, g.enabled, g.last_seen, g.last_change, g.last_backup,
		COUNT(s.id), COALESCE(SUM(s.stored_size), 0)
	FROM games g LEFT JOIN snapshots s ON s.game_id = g.id WHERE g.id = ? GROUP BY g.id`, id)
	game, err := scanGame(row)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Game{}, fmt.Errorf("game %q: %w", id, err)
	}
	return game, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scanGame(row scanner) (core.Game, error) {
	var game core.Game
	var enabled bool
	var lastSeen, lastChange, lastBackup sql.NullInt64
	if err := row.Scan(
		&game.ID, &game.CatalogID, &game.CatalogName, &game.DisplayName, &game.Store, &game.StoreID,
		&game.InstallPath, &game.Image, &game.Notes, &enabled, &lastSeen, &lastChange, &lastBackup,
		&game.SnapshotCount, &game.StoredSize,
	); err != nil {
		return core.Game{}, fmt.Errorf("scan game: %w", err)
	}
	game.Enabled = enabled
	game.LastSeen = timePointer(lastSeen)
	game.LastChange = timePointer(lastChange)
	game.LastBackup = timePointer(lastBackup)
	return game, nil
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
	_, err = s.db.ExecContext(ctx, `UPDATE games SET display_name = ?, image = ?, notes = ?, enabled = ? WHERE id = ?`,
		game.DisplayName, game.Image, game.Notes, game.Enabled, id)
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
	if _, err := tx.ExecContext(ctx, `UPDATE games SET last_backup = ? WHERE id = ?`, snapshot.CreatedAt.UnixMilli(), snapshot.GameID); err != nil {
		return fmt.Errorf("update last backup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
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

func (s *Store) PendingSnapshots(ctx context.Context) (snapshots []core.Snapshot, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT manifest FROM snapshots WHERE remote_state != 'synced' ORDER BY created_at`)
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

func (s *Store) Snapshot(ctx context.Context, id string) (core.Snapshot, error) {
	var data []byte
	if err := s.db.QueryRowContext(ctx, `SELECT manifest FROM snapshots WHERE id = ?`, id).Scan(&data); err != nil {
		return core.Snapshot{}, fmt.Errorf("load snapshot %q: %w", id, err)
	}
	var snapshot core.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return core.Snapshot{}, fmt.Errorf("decode snapshot %q: %w", id, err)
	}
	return snapshot, nil
}

func (s *Store) SaveBlob(ctx context.Context, hash, path string, originalSize, storedSize int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO blobs (hash, local_path, original_size, stored_size)
		VALUES (?, ?, ?, ?) ON CONFLICT(hash) DO NOTHING`, hash, path, originalSize, storedSize)
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
