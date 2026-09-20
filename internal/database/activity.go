package database

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

const ActivityRetention = core.ActivityRetention

func (s *Store) SaveEvent(ctx context.Context, event core.Event) (err error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode activity event: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin activity event transaction: %w", err)
	}
	defer func() {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			err = errors.Join(err, rollbackErr)
		}
	}()
	if _, err := tx.ExecContext(ctx, `INSERT INTO activity_events (timestamp, payload) VALUES (?, ?)`, event.Timestamp.UnixMilli(), payload); err != nil {
		return fmt.Errorf("save activity event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM activity_events WHERE timestamp < ?`, time.Now().UTC().Add(-ActivityRetention).UnixMilli()); err != nil {
		return fmt.Errorf("prune activity events: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit activity event: %w", err)
	}
	return nil
}

func (s *Store) LoadEvents(ctx context.Context, now time.Time) (events []core.Event, err error) {
	cutoff := now.UTC().Add(-ActivityRetention).UnixMilli()
	if err := s.PruneEvents(ctx, now); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM activity_events WHERE timestamp >= ? ORDER BY id`, cutoff)
	if err != nil {
		return nil, fmt.Errorf("load activity events: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("scan activity event: %w", err)
		}
		var event core.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("decode activity event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activity events: %w", err)
	}
	return events, nil
}

func (s *Store) PruneEvents(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM activity_events WHERE timestamp < ?`, now.UTC().Add(-ActivityRetention).UnixMilli()); err != nil {
		return fmt.Errorf("prune activity events: %w", err)
	}
	return nil
}
