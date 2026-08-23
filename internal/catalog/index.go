package catalog

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

const maxCatalogEntrySize = 8 << 20

type Index struct {
	Path string
}

func CompileFile(ctx context.Context, manifestPath, indexPath string) error {
	//nolint:gosec // Both paths are application-owned catalog cache locations.
	manifest, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("open Ludusavi manifest for indexing: %w", err)
	}
	compileErr := Compile(ctx, manifest, indexPath)
	return errors.Join(compileErr, manifest.Close())
}

func EnsureIndex(ctx context.Context, manifestPath, indexPath string) error {
	index := Index{Path: indexPath}
	if _, err := index.Count(ctx); err == nil {
		return nil
	}
	return CompileFile(ctx, manifestPath, indexPath)
}

func Compile(ctx context.Context, source io.Reader, destination string) (err error) {
	database, err := sql.Open("sqlite", destination)
	if err != nil {
		return fmt.Errorf("open catalog index: %w", err)
	}
	database.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, database.Close()) }()
	for _, statement := range catalogSchema {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize catalog index: %w", err)
		}
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin catalog index update: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, transaction.Rollback())
		}
	}()
	for _, table := range []string{"steam_ids", "gog_ids", "install_dirs", "games"} {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM "+table); err != nil { //nolint:gosec // Table names are fixed above.
			return fmt.Errorf("clear catalog table %q: %w", table, err)
		}
	}
	writer, err := newIndexWriter(ctx, transaction)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, writer.Close()) }()
	if err := scanManifestEntries(ctx, source, writer.Add); err != nil {
		return err
	}
	if writer.count == 0 {
		return errors.New("ludusavi manifest did not contain any games")
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit catalog index: %w", err)
	}
	committed = true
	return nil
}

var catalogSchema = []string{
	`PRAGMA busy_timeout = 5000`,
	`PRAGMA journal_mode = WAL`,
	`CREATE TABLE IF NOT EXISTS games (
		name TEXT PRIMARY KEY,
		normalized_name TEXT NOT NULL,
		alias TEXT NOT NULL,
		definition BLOB NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS games_normalized_name ON games(normalized_name)`,
	`CREATE TABLE IF NOT EXISTS steam_ids (id TEXT PRIMARY KEY, game_name TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS gog_ids (id TEXT PRIMARY KEY, game_name TEXT NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS install_dirs (normalized_dir TEXT PRIMARY KEY, game_name TEXT NOT NULL)`,
}

type indexWriter struct {
	ctx        context.Context
	game       *sql.Stmt
	steam      *sql.Stmt
	gog        *sql.Stmt
	installDir *sql.Stmt
	count      int
	closed     bool
}

func newIndexWriter(ctx context.Context, transaction *sql.Tx) (*indexWriter, error) {
	game, err := transaction.PrepareContext(ctx, `INSERT INTO games (name, normalized_name, alias, definition) VALUES (?, ?, ?, ?)`)
	if err != nil {
		return nil, fmt.Errorf("prepare catalog game insert: %w", err)
	}
	steam, err := transaction.PrepareContext(ctx, `INSERT OR REPLACE INTO steam_ids (id, game_name) VALUES (?, ?)`)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare Steam ID insert: %w", err), game.Close())
	}
	gog, err := transaction.PrepareContext(ctx, `INSERT OR REPLACE INTO gog_ids (id, game_name) VALUES (?, ?)`)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare GOG ID insert: %w", err), game.Close(), steam.Close())
	}
	installDir, err := transaction.PrepareContext(ctx, `INSERT OR REPLACE INTO install_dirs (normalized_dir, game_name) VALUES (?, ?)`)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("prepare install directory insert: %w", err), game.Close(), steam.Close(), gog.Close())
	}
	return &indexWriter{ctx: ctx, game: game, steam: steam, gog: gog, installDir: installDir}, nil
}

func (w *indexWriter) Add(name string, definition Definition) error {
	definition.Name = name
	encoded, err := json.Marshal(definition)
	if err != nil {
		return fmt.Errorf("encode catalog game %q: %w", name, err)
	}
	if _, err := w.game.ExecContext(w.ctx, name, normalize(name), definition.Alias, encoded); err != nil {
		return fmt.Errorf("index catalog game %q: %w", name, err)
	}
	for directory := range definition.InstallDir {
		if _, err := w.installDir.ExecContext(w.ctx, normalize(directory), name); err != nil {
			return fmt.Errorf("index install directory for %q: %w", name, err)
		}
	}
	for _, id := range append([]string{definition.Steam.ID}, definition.IDs.SteamExtra...) {
		if id != "" {
			if _, err := w.steam.ExecContext(w.ctx, id, name); err != nil {
				return fmt.Errorf("index Steam ID for %q: %w", name, err)
			}
		}
	}
	for _, id := range append([]string{definition.GOG.ID}, definition.IDs.GOGExtra...) {
		if id != "" {
			if _, err := w.gog.ExecContext(w.ctx, id, name); err != nil {
				return fmt.Errorf("index GOG ID for %q: %w", name, err)
			}
		}
	}
	w.count++
	return nil
}

func (w *indexWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	return errors.Join(w.game.Close(), w.steam.Close(), w.gog.Close(), w.installDir.Close())
}

func scanManifestEntries(ctx context.Context, source io.Reader, consume func(string, Definition) error) error {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64<<10), maxCatalogEntrySize)
	var entry strings.Builder
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if ignoredCatalogLine(trimmed) {
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && entry.Len() > 0 {
			if err := flushCatalogEntry(&entry, consume); err != nil {
				return err
			}
			entry.Reset()
		}
		if entry.Len()+len(line)+1 > maxCatalogEntrySize {
			return fmt.Errorf("ludusavi catalog entry exceeds %d bytes", maxCatalogEntrySize)
		}
		entry.WriteString(line)
		entry.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan Ludusavi manifest: %w", err)
	}
	return flushCatalogEntry(&entry, consume)
}

func ignoredCatalogLine(trimmed string) bool {
	return trimmed == "" || strings.HasPrefix(trimmed, "#") || trimmed == "---" || trimmed == "..."
}

func flushCatalogEntry(entry *strings.Builder, consume func(string, Definition) error) error {
	if entry.Len() == 0 {
		return nil
	}
	var decoded map[string]Definition
	if err := yaml.Unmarshal([]byte(entry.String()), &decoded); err != nil {
		return fmt.Errorf("decode Ludusavi catalog entry: %w", err)
	}
	if len(decoded) != 1 {
		return errors.New("ludusavi catalog entry did not contain exactly one game")
	}
	for name, definition := range decoded {
		if strings.TrimSpace(name) == "" {
			return errors.New("ludusavi catalog contains a game without a name")
		}
		return consume(name, definition)
	}
	return nil
}

func (i Index) Count(ctx context.Context) (count int, err error) {
	database, err := i.open()
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	if err := database.QueryRowContext(ctx, `SELECT COUNT(*) FROM games`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count catalog games: %w", err)
	}
	return count, nil
}

func (i Index) SteamGame(ctx context.Context, id string) (Definition, bool, error) {
	return i.gameByLookup(ctx, `SELECT game_name FROM steam_ids WHERE id = ?`, id)
}

func (i Index) MatchName(ctx context.Context, candidates ...string) (Definition, bool, error) {
	for _, candidate := range candidates {
		normalized := normalize(candidate)
		definition, found, err := i.gameByLookup(ctx, `SELECT game_name FROM install_dirs WHERE normalized_dir = ?`, normalized)
		if err != nil || found {
			return definition, found, err
		}
		definition, found, err = i.gameByLookup(ctx, `SELECT name FROM games WHERE normalized_name = ?`, normalized)
		if err != nil || found {
			return definition, found, err
		}
	}
	return Definition{}, false, nil
}

func (i Index) gameByLookup(ctx context.Context, query, value string) (definition Definition, found bool, err error) {
	database, err := i.open()
	if err != nil {
		return Definition{}, false, err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	var name string
	if err := database.QueryRowContext(ctx, query, value).Scan(&name); errors.Is(err, sql.ErrNoRows) {
		return Definition{}, false, nil
	} else if err != nil {
		return Definition{}, false, fmt.Errorf("query catalog index: %w", err)
	}
	return resolveDatabase(ctx, database, name)
}

func (i Index) Resolve(ctx context.Context, name string) (Definition, bool, error) {
	database, err := i.open()
	if err != nil {
		return Definition{}, false, err
	}
	definition, found, resolveErr := resolveDatabase(ctx, database, name)
	return definition, found, errors.Join(resolveErr, database.Close())
}

func resolveDatabase(ctx context.Context, database *sql.DB, name string) (Definition, bool, error) {
	for range 10 {
		var encoded []byte
		if err := database.QueryRowContext(ctx, `SELECT definition FROM games WHERE name = ?`, name).Scan(&encoded); errors.Is(err, sql.ErrNoRows) {
			return Definition{}, false, nil
		} else if err != nil {
			return Definition{}, false, fmt.Errorf("load catalog game %q: %w", name, err)
		}
		var definition Definition
		if err := json.Unmarshal(encoded, &definition); err != nil {
			return Definition{}, false, fmt.Errorf("decode indexed catalog game %q: %w", name, err)
		}
		if definition.Alias == "" {
			return definition, true, nil
		}
		name = definition.Alias
	}
	return Definition{}, false, errors.New("catalog alias chain exceeds ten entries")
}

func (i Index) Search(ctx context.Context, query string, limit int) (choices []string, err error) {
	database, err := i.open()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	pattern := "%" + strings.ReplaceAll(strings.ReplaceAll(strings.ReplaceAll(query, `\`, `\\`), `%`, `\%`), `_`, `\_`) + "%"
	rows, err := database.QueryContext(ctx, `SELECT name FROM games WHERE alias = '' AND name LIKE ? ESCAPE '\' ORDER BY name COLLATE NOCASE LIMIT ?`, pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("search catalog: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan catalog search result: %w", err)
		}
		choices = append(choices, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate catalog search results: %w", err)
	}
	return choices, nil
}

func (i Index) EachDefinition(ctx context.Context, consume func(string, Definition) error) (err error) {
	database, err := i.open()
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, database.Close()) }()
	rows, err := database.QueryContext(ctx, `SELECT name, definition FROM games WHERE alias = '' ORDER BY name`)
	if err != nil {
		return fmt.Errorf("read catalog definitions: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var name string
		var encoded []byte
		if err := rows.Scan(&name, &encoded); err != nil {
			return fmt.Errorf("scan catalog definition: %w", err)
		}
		var definition Definition
		if err := json.Unmarshal(encoded, &definition); err != nil {
			return fmt.Errorf("decode indexed catalog game %q: %w", name, err)
		}
		if err := consume(name, definition); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate catalog definitions: %w", err)
	}
	return nil
}

func (i Index) open() (*sql.DB, error) {
	if _, err := os.Stat(i.Path); err != nil {
		return nil, fmt.Errorf("open catalog index: %w", err)
	}
	database, err := sql.Open("sqlite", i.Path)
	if err != nil {
		return nil, fmt.Errorf("open catalog index: %w", err)
	}
	database.SetMaxOpenConns(1)
	return database, nil
}
