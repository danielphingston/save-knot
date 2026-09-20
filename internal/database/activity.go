package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/saveknot/saveknot/internal/core"
)

const ActivityRetention = core.ActivityRetention

func (s *Store) SaveEvent(ctx context.Context, event core.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode activity event: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO activity_events (timestamp, payload) VALUES (?, ?)`, event.Timestamp.UnixMilli(), payload); err != nil {
		return fmt.Errorf("save activity event: %w", err)
	}
	return nil
}

func (s *Store) LoadEventPage(ctx context.Context, until time.Time, offset, limit int) (events []core.Event, total int, err error) {
	cutoff := time.Now().UTC().Add(-ActivityRetention).UnixMilli()
	untilMillis := until.UTC().UnixMilli()
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM activity_events WHERE timestamp >= ? AND timestamp <= ?`, cutoff, untilMillis).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count activity events: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM activity_events
		WHERE timestamp >= ? AND timestamp <= ?
		ORDER BY timestamp DESC, id DESC LIMIT ? OFFSET ?`, cutoff, untilMillis, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("load activity event page: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, 0, fmt.Errorf("scan activity event: %w", err)
		}
		var event core.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, 0, fmt.Errorf("decode activity event: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate activity events: %w", err)
	}
	return events, total, nil
}

func (s *Store) PruneEvents(ctx context.Context, now time.Time, eventTypes []string) error {
	cutoff := now.UTC().Add(-ActivityRetention).UnixMilli()
	if len(eventTypes) == 0 {
		if _, err := s.db.ExecContext(ctx, `DELETE FROM activity_events WHERE timestamp < ?`, cutoff); err != nil {
			return fmt.Errorf("prune activity events: %w", err)
		}
		return nil
	}
	typesJSON, err := json.Marshal(eventTypes)
	if err != nil {
		return fmt.Errorf("encode transient activity types: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM activity_events
		WHERE timestamp < ? OR json_extract(payload, '$.type') IN (SELECT value FROM json_each(?))`, cutoff, typesJSON); err != nil {
		return fmt.Errorf("prune activity events: %w", err)
	}
	return nil
}
