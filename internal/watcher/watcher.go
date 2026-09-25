package watcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/saveknot/saveknot/internal/core"
)

type Target struct {
	GameID string
	Path   string
	Policy core.BackupPolicy
}

type pendingChange struct {
	first time.Time
	due   time.Time
}

type Manager struct {
	watcher *fsnotify.Watcher
	update  chan []Target
	jobs    chan string
	done    chan struct{}
	once    sync.Once
}

func New() (*Manager, error) {
	watched, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create filesystem watcher: %w", err)
	}
	return &Manager{
		watcher: watched,
		update:  make(chan []Target, 1),
		jobs:    make(chan string, 32),
		done:    make(chan struct{}),
	}, nil
}

func (m *Manager) Reconcile(targets []Target) {
	select {
	case m.update <- targets:
	default:
		select {
		case <-m.update:
		default:
		}
		m.update <- targets
	}
}

func (m *Manager) Start(ctx context.Context, backup func(context.Context, string)) {
	go m.run(ctx)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case gameID := <-m.jobs:
				backup(ctx, gameID)
			}
		}
	}()
}

func (m *Manager) Close() error {
	var err error
	m.once.Do(func() {
		close(m.done)
		err = m.watcher.Close()
	})
	return err
}

func (m *Manager) run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	targets := make(map[string][]Target)
	pending := make(map[string]pendingChange)
	lastQueued := make(map[string]time.Time)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.done:
			return
		case revised := <-m.update:
			targets = m.applyTargets(revised)
			prunePending(revised, pending)
		case event, ok := <-m.watcher.Events:
			if !ok {
				return
			}
			m.handleEvent(event, targets, pending, lastQueued)
		case <-ticker.C:
			m.flushPending(time.Now(), pending, lastQueued)
		case err, ok := <-m.watcher.Errors:
			if ok {
				slog.Warn("filesystem watcher", "error", err)
			}
		}
	}
}

func prunePending(targets []Target, pending map[string]pendingChange) {
	activeGames := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		activeGames[target.GameID] = struct{}{}
	}
	for gameID := range pending {
		if _, active := activeGames[gameID]; !active {
			delete(pending, gameID)
		}
	}
}

func (m *Manager) handleEvent(event fsnotify.Event, targets map[string][]Target, pending map[string]pendingChange, lastQueued map[string]time.Time) {
	if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			if err := m.addTree(event.Name); err != nil {
				slog.Warn("watch new save directory", "path", event.Name, "error", err)
			}
		}
	}
	now := time.Now()
	for root, rootTargets := range targets {
		if !within(root, event.Name) {
			continue
		}
		for _, target := range rootTargets {
			change, exists := pending[target.GameID]
			if !exists {
				change.first = now
			}
			change.due = snapshotDue(now, change.first, lastQueued[target.GameID], target.Policy)
			pending[target.GameID] = change
		}
	}
}

func (m *Manager) flushPending(now time.Time, pending map[string]pendingChange, lastQueued map[string]time.Time) {
	for gameID, change := range pending {
		if now.Before(change.due) {
			continue
		}
		select {
		case m.jobs <- gameID:
			lastQueued[gameID] = now
			delete(pending, gameID)
		default:
		}
	}
}

func snapshotDue(now, first, lastQueued time.Time, policy core.BackupPolicy) time.Time {
	if policy.QuietSeconds < 1 || policy.MaxDirtySeconds < policy.QuietSeconds || policy.MinGapSeconds < 0 {
		policy = core.DefaultBackupPolicy()
	}
	due := now.Add(time.Duration(policy.QuietSeconds) * time.Second)
	if minimum := lastQueued.Add(time.Duration(policy.MinGapSeconds) * time.Second); !lastQueued.IsZero() && minimum.After(due) {
		due = minimum
	}
	return earlier(due, first.Add(time.Duration(policy.MaxDirtySeconds)*time.Second))
}

func (m *Manager) applyTargets(revised []Target) map[string][]Target {
	targets := make(map[string][]Target)
	desired := make(map[string]struct{})
	for _, target := range revised {
		root, err := nearestExistingDirectory(target.Path)
		if err != nil {
			continue
		}
		targets[root] = append(targets[root], target)
		if err := m.addTarget(root, target.Path); err != nil {
			slog.Warn("watch save directory", "path", root, "error", err)
		}
		if err := collectTargetWatches(target.Path, root, desired); err != nil {
			slog.Warn("collect save directory watches", "path", target.Path, "error", err)
		}
	}
	for _, path := range m.watcher.WatchList() {
		if _, keep := desired[path]; keep {
			continue
		}
		if err := m.watcher.Remove(path); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			slog.Warn("remove obsolete filesystem watch", "path", path, "error", err)
		}
	}
	return targets
}

func (m *Manager) addTarget(root, target string) error {
	info, err := os.Stat(target)
	if err == nil && info.IsDir() {
		return m.addTree(root)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return m.watcher.Add(root)
}

func (m *Manager) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return fs.SkipDir
			}
			return walkErr
		}
		if entry.IsDir() {
			if err := m.watcher.Add(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	})
}

func collectTargetWatches(target, nearest string, directories map[string]struct{}) error {
	info, err := os.Stat(target)
	if err != nil || !info.IsDir() {
		directories[nearest] = struct{}{}
		return nil
	}
	return filepath.WalkDir(target, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrPermission) {
				return fs.SkipDir
			}
			return walkErr
		}
		if entry.IsDir() {
			directories[path] = struct{}{}
		}
		return nil
	})
}

func nearestExistingDirectory(path string) (string, error) {
	current := filepath.Clean(path)
	for {
		info, err := os.Stat(current)
		if err == nil {
			if info.IsDir() {
				if filepath.Dir(current) == current {
					return "", errors.New("refusing to watch a filesystem root")
				}
				return current, nil
			}
			return filepath.Dir(current), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", os.ErrNotExist
		}
		current = parent
	}
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func earlier(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}
