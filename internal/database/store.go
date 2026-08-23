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
	if err := s.ensureSyncEnabledColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureHiddenColumn(ctx); err != nil {
		return err
	}
	return nil
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
		COALESCE(SUM(CASE WHEN s.remote_state != 'synced' THEN 1 ELSE 0 END), 0)
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
		COALESCE(SUM(CASE WHEN s.remote_state != 'synced' THEN 1 ELSE 0 END), 0)
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
		&game.SnapshotCount, &game.StoredSize, &game.PendingCount,
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
	if _, err := tx.ExecContext(ctx, `UPDATE games SET last_backup = ?, last_change = ? WHERE id = ?`, snapshot.CreatedAt.UnixMilli(), snapshot.CreatedAt.UnixMilli(), snapshot.GameID); err != nil {
		return fmt.Errorf("update last backup: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit snapshot: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) ImportSnapshot(ctx context.Context, snapshot core.Snapshot) error {
	snapshot.RemoteState = "synced"
	manifest, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode imported snapshot: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO snapshots
		(id, game_id, device_id, created_at, file_count, original_size, stored_size, remote_state, manifest)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'synced', ?)
		ON CONFLICT(id) DO UPDATE SET remote_state = 'synced', manifest = excluded.manifest`,
		snapshot.ID, snapshot.GameID, snapshot.DeviceID, snapshot.CreatedAt.UnixMilli(), len(snapshot.Files),
		snapshot.OriginalSize, snapshot.StoredSize, manifest)
	if err != nil {
		return fmt.Errorf("import remote snapshot %q: %w", snapshot.ID, err)
	}
	timestamp := snapshot.CreatedAt.UnixMilli()
	if _, err := s.db.ExecContext(ctx, `UPDATE games SET
		last_backup = CASE WHEN last_backup IS NULL OR last_backup < ? THEN ? ELSE last_backup END,
		last_change = CASE WHEN last_change IS NULL OR last_change < ? THEN ? ELSE last_change END
		WHERE id = ?`, timestamp, timestamp, timestamp, timestamp, snapshot.GameID); err != nil {
		return fmt.Errorf("update remote game backup time %q: %w", snapshot.GameID, err)
	}
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

func (s *Store) DeleteSnapshot(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM snapshots WHERE id = ?`, id)
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
