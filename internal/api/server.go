package api

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/events"
	"github.com/saveknot/saveknot/internal/settings"
)

const (
	maxJSONBody = 1 << 20
	maxImage    = 5 << 20
)

//go:embed web
var webFiles embed.FS

type gameReader interface {
	ListGames(context.Context) ([]core.Game, error)
	Game(context.Context, string) (core.Game, error)
	GamePaths(context.Context, string) ([]core.GamePath, error)
	ListSnapshots(context.Context, string) ([]core.Snapshot, error)
}

type gameWriter interface {
	UpsertGame(context.Context, core.Game) error
	AddPath(context.Context, core.GamePath) error
	UpdateGame(context.Context, string, core.GameUpdate) error
}

type coordinator interface {
	Backup(context.Context, string) (core.Snapshot, error)
	Restore(context.Context, string, string) (core.Snapshot, error)
	ReconcileWatches(context.Context)
	SyncPending(context.Context) error
}

type Server struct {
	reader      gameReader
	writer      gameWriter
	coordinator coordinator
	settings    *settings.Manager
	events      *events.Bus
	artworkDir  string
	http        *http.Server
}

func New(
	listen string,
	reader gameReader,
	writer gameWriter,
	coordinator coordinator,
	settings *settings.Manager,
	eventBus *events.Bus,
	artworkDir string,
) (*Server, error) {
	if err := validateLoopback(listen); err != nil {
		return nil, err
	}
	server := &Server{
		reader: reader, writer: writer, coordinator: coordinator, settings: settings,
		events: eventBus, artworkDir: artworkDir,
	}
	mux := http.NewServeMux()
	server.routes(mux)
	server.http = &http.Server{
		Addr: listen, Handler: securityHeaders(mux), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 15 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second,
	}
	return server, nil
}

func (s *Server) Start() <-chan error {
	errorsChannel := make(chan error, 1)
	go func() {
		err := s.http.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errorsChannel <- err
	}()
	return errorsChannel
}

func (s *Server) Close(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", s.status)
	mux.HandleFunc("GET /api/games", s.listGames)
	mux.HandleFunc("POST /api/games", s.addGame)
	mux.HandleFunc("GET /api/games/{id}", s.game)
	mux.HandleFunc("PATCH /api/games/{id}", s.updateGame)
	mux.HandleFunc("POST /api/games/{id}/paths", s.addPath)
	mux.HandleFunc("POST /api/games/{id}/image", s.uploadImage)
	mux.HandleFunc("GET /api/games/{id}/snapshots", s.listSnapshots)
	mux.HandleFunc("POST /api/games/{id}/backup", s.backup)
	mux.HandleFunc("POST /api/games/{id}/restore/{snapshot}", s.restore)
	mux.HandleFunc("POST /api/r2", s.configureR2)
	mux.HandleFunc("GET /api/events", s.eventStream)
	mux.Handle("GET /artwork/", http.StripPrefix("/artwork/", http.FileServer(http.Dir(s.artworkDir))))
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /", spaHandler(assets))
}

func (s *Server) status(writer http.ResponseWriter, request *http.Request) {
	cfg := s.settings.Config()
	writeJSON(writer, http.StatusOK, map[string]any{
		"deviceId":     cfg.DeviceID,
		"r2Configured": cfg.R2.CredentialID != "",
		"r2":           map[string]string{"accountId": cfg.R2.AccountID, "bucket": cfg.R2.Bucket, "prefix": cfg.R2.Prefix, "accessKeyId": cfg.R2.AccessKeyID},
	})
}

func (s *Server) listGames(writer http.ResponseWriter, request *http.Request) {
	games, err := s.reader.ListGames(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if games == nil {
		games = []core.Game{}
	}
	writeJSON(writer, http.StatusOK, games)
}

func (s *Server) game(writer http.ResponseWriter, request *http.Request) {
	game, err := s.reader.Game(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusNotFound, err)
		return
	}
	paths, err := s.reader.GamePaths(request.Context(), game.ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"game": game, "paths": paths})
}

func (s *Server) addGame(writer http.ResponseWriter, request *http.Request) {
	var input core.ManualGame
	if !decodeJSON(writer, request, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Paths) == 0 {
		writeError(writer, http.StatusBadRequest, errors.New("name and at least one save location are required"))
		return
	}
	resolvedPaths := make([]string, 0, len(input.Paths))
	for _, candidate := range input.Paths {
		resolved := filepath.Clean(strings.TrimSpace(candidate))
		if !filepath.IsAbs(resolved) {
			writeError(writer, http.StatusBadRequest, fmt.Errorf("save location %q must be an absolute path", resolved))
			return
		}
		resolvedPaths = append(resolvedPaths, resolved)
	}
	now := time.Now().UTC()
	gameID, err := core.NewID(now)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	game := core.Game{ID: gameID, DisplayName: input.Name, Store: "custom", Image: input.Image, Enabled: true, LastSeen: &now}
	if err := s.writer.UpsertGame(request.Context(), game); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	for _, resolved := range resolvedPaths {
		pathID, err := core.NewID(time.Now().UTC())
		if err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
		path := core.GamePath{ID: pathID, GameID: gameID, Source: "custom", Template: resolved, Resolved: resolved, Enabled: true}
		if err := s.writer.AddPath(request.Context(), path); err != nil {
			writeError(writer, http.StatusInternalServerError, err)
			return
		}
	}
	s.coordinator.ReconcileWatches(request.Context())
	writeJSON(writer, http.StatusCreated, game)
}

func (s *Server) updateGame(writer http.ResponseWriter, request *http.Request) {
	var input core.GameUpdate
	if !decodeJSON(writer, request, &input) {
		return
	}
	if input.DisplayName != nil && strings.TrimSpace(*input.DisplayName) == "" {
		writeError(writer, http.StatusBadRequest, errors.New("display name cannot be empty"))
		return
	}
	if err := s.writer.UpdateGame(request.Context(), request.PathValue("id"), input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.coordinator.ReconcileWatches(request.Context())
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) addPath(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Path string `json:"path"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	resolved := filepath.Clean(strings.TrimSpace(input.Path))
	if !filepath.IsAbs(resolved) {
		writeError(writer, http.StatusBadRequest, errors.New("save location must be an absolute path"))
		return
	}
	id, err := core.NewID(time.Now().UTC())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	path := core.GamePath{ID: id, GameID: request.PathValue("id"), Source: "custom", Template: resolved, Resolved: resolved, Enabled: true}
	if err := s.writer.AddPath(request.Context(), path); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.coordinator.ReconcileWatches(request.Context())
	writeJSON(writer, http.StatusCreated, path)
}

func (s *Server) listSnapshots(writer http.ResponseWriter, request *http.Request) {
	snapshots, err := s.reader.ListSnapshots(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if snapshots == nil {
		snapshots = []core.Snapshot{}
	}
	writeJSON(writer, http.StatusOK, snapshots)
}

func (s *Server) backup(writer http.ResponseWriter, request *http.Request) {
	snapshot, err := s.coordinator.Backup(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusCreated, snapshot)
}

func (s *Server) restore(writer http.ResponseWriter, request *http.Request) {
	preRestore, err := s.coordinator.Restore(request.Context(), request.PathValue("id"), request.PathValue("snapshot"))
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"restored": true, "preRestoreSnapshot": preRestore})
}

func (s *Server) configureR2(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		AccountID       string `json:"accountId"`
		Bucket          string `json:"bucket"`
		Prefix          string `json:"prefix"`
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	settings := config.R2{AccountID: strings.TrimSpace(input.AccountID), Bucket: strings.TrimSpace(input.Bucket), Prefix: input.Prefix, AccessKeyID: strings.TrimSpace(input.AccessKeyID)}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	if err := s.settings.ConfigureR2(ctx, settings, input.SecretAccessKey); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	response := map[string]any{"connected": true}
	if err := s.coordinator.SyncPending(ctx); err != nil {
		response["syncWarning"] = err.Error()
	}
	writeJSON(writer, http.StatusOK, response)
}

func (s *Server) eventStream(writer http.ResponseWriter, request *http.Request) {
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	eventChannel, unsubscribe := s.events.Subscribe(16)
	defer unsubscribe()
	for {
		select {
		case <-request.Context().Done():
			return
		case event := <-eventChannel:
			data, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) uploadImage(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxImage)
	file, header, err := request.FormFile("image")
	if err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("read image: %w", err))
		return
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			slog.Warn("close uploaded artwork", "error", closeErr)
		}
	}()
	extension, err := imageExtension(header, file)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	if err := os.MkdirAll(s.artworkDir, 0o700); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("create artwork directory: %w", err))
		return
	}
	gameID := request.PathValue("id")
	if !safeIDPattern.MatchString(gameID) {
		writeError(writer, http.StatusBadRequest, errors.New("invalid game ID"))
		return
	}
	filename := gameID + extension
	destination := filepath.Join(s.artworkDir, filename)
	temporary, err := os.CreateTemp(s.artworkDir, "artwork-*")
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	temporaryPath := temporary.Name()
	_, copyErr := io.Copy(temporary, file)
	closeErr := temporary.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("save artwork: %w", errors.Join(err, removeTemporary(temporaryPath))))
		return
	}
	//nolint:gosec // gameID is restricted to a single safe filename segment above.
	if err := os.Rename(temporaryPath, destination); err != nil {
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("replace artwork: %w", errors.Join(err, removeTemporary(temporaryPath))))
		return
	}
	imageURL := "/artwork/" + filename
	if err := s.writer.UpdateGame(request.Context(), gameID, core.GameUpdate{Image: &imageURL}); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"image": imageURL})
}

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func removeTemporary(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func imageExtension(header *multipart.FileHeader, file multipart.File) (string, error) {
	buffer := make([]byte, 512)
	count, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("inspect image: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("rewind image: %w", err)
	}
	contentType := http.DetectContentType(buffer[:count])
	switch contentType {
	case "image/jpeg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/gif":
		return ".gif", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("unsupported image type %q for %q", contentType, header.Filename)
	}
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxJSONBody)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("decode request: %w", err))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(writer, http.StatusBadRequest, errors.New("request body must contain exactly one JSON value"))
		return false
	}
	return true
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		slog.Error("write HTTP response", "error", err)
	}
}

func writeError(writer http.ResponseWriter, status int, err error) {
	writeJSON(writer, status, map[string]string{"error": err.Error()})
}

func validateLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address: %w", err)
	}
	if host != "localhost" {
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return errors.New("SaveKnot may only listen on a loopback address")
		}
	}
	return nil
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; style-src 'self'; script-src 'self'; connect-src 'self'")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}

func spaHandler(assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		path := strings.TrimPrefix(request.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err != nil {
			request.URL.Path = "/"
		}
		files.ServeHTTP(writer, request)
	})
}
