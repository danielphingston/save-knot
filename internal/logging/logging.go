package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

const (
	maxLogSize = 5 << 20
	logBackups = 3
)

type rotatingFile struct {
	mu   sync.Mutex
	path string
	file *os.File
	size int64
}

func Configure(dataDir, level string) (io.Closer, string, error) {
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, "", fmt.Errorf("create log directory: %w", err)
	}
	path := filepath.Join(logDir, "saveknot.log")
	file, err := openRotating(path)
	if err != nil {
		return nil, "", err
	}
	parsedLevel, err := parseLevel(level)
	if err != nil {
		closeErr := file.Close()
		return nil, "", errors.Join(err, closeErr)
	}
	handler := slog.NewTextHandler(io.MultiWriter(os.Stderr, file), &slog.HandlerOptions{Level: parsedLevel})
	slog.SetDefault(slog.New(handler))
	return file, path, nil
}

func parseLevel(value string) (slog.Level, error) {
	var level slog.Level
	if value == "" {
		return slog.LevelInfo, nil
	}
	if err := level.UnmarshalText([]byte(value)); err != nil {
		return 0, fmt.Errorf("invalid log level %q (use debug, info, warn, or error): %w", value, err)
	}
	return level, nil
}

func openRotating(path string) (*rotatingFile, error) {
	//nolint:gosec // This is an application-owned log path selected during bootstrap.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("inspect log file: %w", err), file.Close())
	}
	return &rotatingFile{path: path, file: file, size: info.Size()}, nil
}

func (r *rotatingFile) Write(data []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(data)) > maxLogSize {
		if err := r.rotate(); err != nil {
			return 0, err
		}
	}
	written, err := r.file.Write(data)
	r.size += int64(written)
	return written, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.file.Close()
}

func (r *rotatingFile) rotate() error {
	if err := r.file.Close(); err != nil {
		return fmt.Errorf("close full log file: %w", err)
	}
	for index := logBackups - 1; index >= 1; index-- {
		older := fmt.Sprintf("%s.%d", r.path, index)
		newer := fmt.Sprintf("%s.%d", r.path, index+1)
		if err := os.Rename(older, newer); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("rotate log file %q: %w", older, err)
		}
	}
	if err := os.Rename(r.path, r.path+".1"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rotate active log file: %w", err)
	}
	file, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open rotated log file: %w", err)
	}
	r.file = file
	r.size = 0
	return nil
}
