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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/core"
	"github.com/saveknot/saveknot/internal/events"
	"github.com/saveknot/saveknot/internal/platform"
	"github.com/saveknot/saveknot/internal/remote"
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
	ListHiddenGames(context.Context) ([]core.Game, error)
	Game(context.Context, string) (core.Game, error)
	ListSnapshots(context.Context, string) ([]core.Snapshot, error)
}

type sourceReader interface {
	GamePaths(context.Context, string) ([]core.GamePath, error)
	GameRegistry(context.Context, string) ([]core.RegistryPath, error)
	ListExclusions(context.Context, string) ([]core.GameExclusion, error)
	GamePolicy(context.Context, string) (core.BackupPolicy, error)
}

type gameWriter interface {
	UpsertGame(context.Context, core.Game) error
	AddPath(context.Context, core.GamePath) error
	UpdateGame(context.Context, string, core.GameUpdate) error
}

type pathWriter interface {
	UpdatePath(context.Context, string, string, bool) error
	DeletePath(context.Context, string, string) error
	AddExclusion(context.Context, core.GameExclusion) error
	DeleteExclusion(context.Context, string, string) error
	UpdateGamePolicy(context.Context, string, core.BackupPolicy) error
}

type backupCoordinator interface {
	Backup(context.Context, string) (core.Snapshot, error)
	Restore(context.Context, string, string) (core.Snapshot, error)
	ReconcileWatches(context.Context)
	DeleteSnapshot(context.Context, string, string, bool) error
	SetGameHidden(context.Context, string, bool) error
}

type syncCoordinator interface {
	SyncNow(context.Context) (core.SyncResult, error)
	SyncInProgress() bool
	SyncGame(context.Context, string) error
	ReconcileRemote(context.Context) (remote.ReconcileResult, error)
}

type discoveryCoordinator interface {
	DiscoverNow(context.Context) core.Diagnostics
	ConfigureLocal(context.Context, config.Local) error
	Diagnostics() core.Diagnostics
	SearchCatalog(context.Context, string) ([]core.CatalogChoice, error)
	RemapGame(context.Context, string, string) error
}

type Server struct {
	reader     gameReader
	sources    sourceReader
	writer     gameWriter
	pathWriter pathWriter
	backups    backupCoordinator
	sync       syncCoordinator
	discovery  discoveryCoordinator
	settings   *settings.Manager
	events     *events.Bus
	artworkDir string
	http       *http.Server
}

func New(
	listen string,
	reader gameReader,
	sources sourceReader,
	writer gameWriter,
	pathWriter pathWriter,
	backup backupCoordinator,
	sync syncCoordinator,
	discovery discoveryCoordinator,
	settings *settings.Manager,
	eventBus *events.Bus,
	artworkDir string,
) (*Server, error) {
	if err := validateLoopback(listen); err != nil {
		return nil, err
	}
	server := &Server{
		reader: reader, sources: sources, writer: writer, pathWriter: pathWriter, backups: backup, sync: sync, discovery: discovery, settings: settings,
		events: eventBus, artworkDir: artworkDir,
	}
	mux := http.NewServeMux()
	server.routes(mux)
	server.http = &http.Server{
		Addr: listen, Handler: securityHeaders(localRequestsOnly(mux)), ReadHeaderTimeout: 5 * time.Second,
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
	mux.HandleFunc("GET /api/games/ignored", s.listHiddenGames)
	mux.HandleFunc("POST /api/games", s.addGame)
	mux.HandleFunc("GET /api/games/{id}", s.game)
	mux.HandleFunc("PATCH /api/games/{id}", s.updateGame)
	mux.HandleFunc("DELETE /api/games/{id}", s.hideGame)
	mux.HandleFunc("POST /api/games/{id}/restore-library", s.restoreGame)
	mux.HandleFunc("POST /api/games/{id}/paths", s.addPath)
	mux.HandleFunc("PATCH /api/games/{id}/paths/{path}", s.updatePath)
	mux.HandleFunc("DELETE /api/games/{id}/paths/{path}", s.deletePath)
	mux.HandleFunc("POST /api/games/{id}/exclusions", s.addExclusion)
	mux.HandleFunc("DELETE /api/games/{id}/exclusions/{exclusion}", s.deleteExclusion)
	mux.HandleFunc("PUT /api/games/{id}/policy", s.updatePolicy)
	mux.HandleFunc("POST /api/games/{id}/image", s.uploadImage)
	mux.HandleFunc("DELETE /api/games/{id}/image", s.resetImage)
	mux.HandleFunc("GET /api/games/{id}/snapshots", s.listSnapshots)
	mux.HandleFunc("POST /api/games/{id}/backup", s.backup)
	mux.HandleFunc("POST /api/games/{id}/sync", s.syncGame)
	mux.HandleFunc("POST /api/games/{id}/restore/{snapshot}", s.restore)
	mux.HandleFunc("DELETE /api/games/{id}/snapshots/{snapshot}", s.deleteSnapshot)
	mux.HandleFunc("POST /api/discovery", s.discover)
	mux.HandleFunc("GET /api/catalog", s.searchCatalog)
	mux.HandleFunc("PUT /api/games/{id}/remap", s.remapGame)
	mux.HandleFunc("POST /api/folder", s.selectFolder)
	mux.HandleFunc("PUT /api/settings/local", s.configureLocal)
	mux.HandleFunc("PUT /api/settings/autostart", s.configureAutostart)
	mux.HandleFunc("PUT /api/settings/retention", s.configureRetention)
	mux.HandleFunc("PUT /api/settings/automation", s.configureAutomation)
	mux.HandleFunc("POST /api/sync", s.syncAll)
	mux.HandleFunc("POST /api/r2/reconcile", s.reconcileRemote)
	mux.HandleFunc("POST /api/r2", s.configureR2)
	mux.HandleFunc("DELETE /api/r2", s.disconnectR2)
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
	r2State := "disconnected"
	if cfg.R2.CredentialID != "" {
		r2State = "configured"
		if cfg.R2.LastFailureAt != nil && (cfg.R2.LastVerifiedAt == nil || cfg.R2.LastFailureAt.After(*cfg.R2.LastVerifiedAt)) {
			r2State = "unavailable"
		} else if cfg.R2.LastVerifiedAt != nil {
			r2State = "verified"
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"deviceId":       cfg.DeviceID,
		"r2Configured":   cfg.R2.CredentialID != "",
		"r2State":        r2State,
		"syncInProgress": s.sync.SyncInProgress(),
		"r2LastSynced":   cfg.R2.LastSyncedAt,
		"r2LastVerified": cfg.R2.LastVerifiedAt,
		"r2LastFailure":  cfg.R2.LastFailureAt,
		"r2LastError":    cfg.R2.LastError,
		"r2":             map[string]string{"accountId": cfg.R2.AccountID, "bucket": cfg.R2.Bucket, "prefix": cfg.R2.Prefix, "accessKeyId": cfg.R2.AccessKeyID},
		"localBackupDir": cfg.LocalBackupDir,
		"steamRoots":     cfg.SteamRoots,
		"epicManifests":  cfg.EpicManifests,
		"gogRoots":       cfg.GOGRoots,
		"launchAtLogin":  cfg.LaunchAtLogin,
		"retentionKeep":  cfg.RetentionKeep,
		"automation":     cfg.Automation,
		"diagnostics":    s.discovery.Diagnostics(),
	})
}

func (s *Server) listHiddenGames(writer http.ResponseWriter, request *http.Request) {
	games, err := s.reader.ListHiddenGames(request.Context())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if games == nil {
		games = []core.Game{}
	}
	writeJSON(writer, http.StatusOK, games)
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
		if errors.Is(err, core.ErrGameNotFound) {
			writeError(writer, http.StatusNotFound, core.ErrGameNotFound)
		} else {
			writeError(writer, http.StatusInternalServerError, err)
		}
		return
	}
	if game.Hidden {
		writeError(writer, http.StatusNotFound, core.ErrGameNotFound)
		return
	}
	paths, err := s.sources.GamePaths(request.Context(), game.ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	registryPaths, err := s.sources.GameRegistry(request.Context(), game.ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	exclusions, err := s.sources.ListExclusions(request.Context(), game.ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	policy, err := s.sources.GamePolicy(request.Context(), game.ID)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if paths == nil {
		paths = []core.GamePath{}
	}
	if registryPaths == nil {
		registryPaths = []core.RegistryPath{}
	}
	if exclusions == nil {
		exclusions = []core.GameExclusion{}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"game": game, "paths": paths, "registry": registryPaths, "exclusions": exclusions, "policy": policy})
}

func (s *Server) hideGame(writer http.ResponseWriter, request *http.Request) {
	if err := s.backups.SetGameHidden(request.Context(), request.PathValue("id"), true); err != nil {
		if errors.Is(err, core.ErrGameNotFound) {
			writeError(writer, http.StatusNotFound, core.ErrGameNotFound)
		} else {
			writeError(writer, http.StatusInternalServerError, err)
		}
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) restoreGame(writer http.ResponseWriter, request *http.Request) {
	if err := s.backups.SetGameHidden(request.Context(), request.PathValue("id"), false); err != nil {
		if errors.Is(err, core.ErrGameNotFound) {
			writeError(writer, http.StatusNotFound, core.ErrGameNotFound)
		} else {
			writeError(writer, http.StatusInternalServerError, err)
		}
		return
	}
	writer.WriteHeader(http.StatusNoContent)
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
	game := core.Game{ID: gameID, DisplayName: input.Name, Store: "custom", Image: input.Image, Enabled: true, SyncEnabled: true, LastSeen: &now}
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
	s.backups.ReconcileWatches(request.Context())
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
	s.backups.ReconcileWatches(request.Context())
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
	s.backups.ReconcileWatches(request.Context())
	writeJSON(writer, http.StatusCreated, path)
}

func (s *Server) updatePath(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := s.pathWriter.UpdatePath(request.Context(), request.PathValue("id"), request.PathValue("path"), input.Enabled); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.backups.ReconcileWatches(request.Context())
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) deletePath(writer http.ResponseWriter, request *http.Request) {
	if err := s.pathWriter.DeletePath(request.Context(), request.PathValue("id"), request.PathValue("path")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.backups.ReconcileWatches(request.Context())
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) addExclusion(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Pattern string `json:"pattern"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	input.Pattern = strings.TrimSpace(input.Pattern)
	if input.Pattern == "" {
		writeError(writer, http.StatusBadRequest, errors.New("exclusion pattern is required"))
		return
	}
	if _, err := doublestar.Match(filepath.ToSlash(input.Pattern), "validation"); err != nil {
		writeError(writer, http.StatusBadRequest, fmt.Errorf("invalid exclusion glob: %w", err))
		return
	}
	id, err := core.NewID(time.Now().UTC())
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	exclusion := core.GameExclusion{ID: id, GameID: request.PathValue("id"), Pattern: input.Pattern}
	if err := s.pathWriter.AddExclusion(request.Context(), exclusion); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writeJSON(writer, http.StatusCreated, exclusion)
}

func (s *Server) deleteExclusion(writer http.ResponseWriter, request *http.Request) {
	if err := s.pathWriter.DeleteExclusion(request.Context(), request.PathValue("id"), request.PathValue("exclusion")); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) updatePolicy(writer http.ResponseWriter, request *http.Request) {
	var policy core.BackupPolicy
	if !decodeJSON(writer, request, &policy) {
		return
	}
	if err := s.pathWriter.UpdateGamePolicy(request.Context(), request.PathValue("id"), policy); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	s.backups.ReconcileWatches(request.Context())
	writer.WriteHeader(http.StatusNoContent)
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
	snapshot, err := s.backups.Backup(request.Context(), request.PathValue("id"))
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusCreated, snapshot)
}

func (s *Server) syncGame(writer http.ResponseWriter, request *http.Request) {
	if err := s.sync.SyncGame(request.Context(), request.PathValue("id")); err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) syncAll(writer http.ResponseWriter, request *http.Request) {
	result, err := s.sync.SyncNow(request.Context())
	if errors.Is(err, core.ErrSyncInProgress) {
		writeError(writer, http.StatusConflict, err)
		return
	}
	if err != nil && result.CheckedGames == 0 && result.Eligible == 0 {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	status := http.StatusOK
	if err != nil {
		status = http.StatusMultiStatus
		result.Error = err.Error()
	}
	writeJSON(writer, status, result)
}

func (s *Server) reconcileRemote(writer http.ResponseWriter, request *http.Request) {
	result, err := s.sync.ReconcileRemote(request.Context())
	if err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (s *Server) deleteSnapshot(writer http.ResponseWriter, request *http.Request) {
	remoteToo := request.URL.Query().Get("remote") != "false"
	if err := s.backups.DeleteSnapshot(request.Context(), request.PathValue("id"), request.PathValue("snapshot"), remoteToo); err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) discover(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, http.StatusOK, s.discovery.DiscoverNow(request.Context()))
}

func (s *Server) searchCatalog(writer http.ResponseWriter, request *http.Request) {
	choices, err := s.discovery.SearchCatalog(request.Context(), request.URL.Query().Get("q"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writeJSON(writer, http.StatusOK, choices)
}

func (s *Server) remapGame(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		CatalogID string `json:"catalogId"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := s.discovery.RemapGame(request.Context(), request.PathValue("id"), strings.TrimSpace(input.CatalogID)); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) selectFolder(writer http.ResponseWriter, request *http.Request) {
	selected, err := platform.SelectFolder(request.Context())
	if err != nil {
		if errors.Is(err, platform.ErrCanceled) {
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]string{"path": selected})
}

func (s *Server) configureLocal(writer http.ResponseWriter, request *http.Request) {
	var input config.Local
	if !decodeJSON(writer, request, &input) {
		return
	}
	input.LocalBackupDir = strings.TrimSpace(input.LocalBackupDir)
	if err := s.discovery.ConfigureLocal(request.Context(), input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) configureAutostart(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := s.settings.ConfigureAutostart(input.Enabled); err != nil {
		writeError(writer, http.StatusUnprocessableEntity, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) configureRetention(writer http.ResponseWriter, request *http.Request) {
	var input struct {
		Keep int `json:"keep"`
	}
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := s.settings.ConfigureRetention(input.Keep); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) configureAutomation(writer http.ResponseWriter, request *http.Request) {
	var input config.Automation
	if !decodeJSON(writer, request, &input) {
		return
	}
	if err := s.settings.ConfigureAutomation(input); err != nil {
		writeError(writer, http.StatusBadRequest, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (s *Server) restore(writer http.ResponseWriter, request *http.Request) {
	preRestore, err := s.backups.Restore(request.Context(), request.PathValue("id"), request.PathValue("snapshot"))
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
	writeJSON(writer, http.StatusOK, map[string]any{"verified": true})
}

func (s *Server) disconnectR2(writer http.ResponseWriter, _ *http.Request) {
	if err := s.settings.DisconnectR2(); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
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
	s.cleanupObsoleteArtwork(gameID, filename)
	writeJSON(writer, http.StatusOK, map[string]string{"image": imageURL})
}

func (s *Server) resetImage(writer http.ResponseWriter, request *http.Request) {
	gameID := request.PathValue("id")
	game, err := s.reader.Game(request.Context(), gameID)
	if err != nil {
		if errors.Is(err, core.ErrGameNotFound) {
			writeError(writer, http.StatusNotFound, core.ErrGameNotFound)
		} else {
			writeError(writer, http.StatusInternalServerError, err)
		}
		return
	}
	filename, custom := localArtworkFilename(gameID, game.Image)
	if !custom {
		writeError(writer, http.StatusBadRequest, errors.New("this game does not have a custom picture"))
		return
	}
	defaultImage := defaultGameImage(game)
	if err := s.writer.UpdateGame(request.Context(), gameID, core.GameUpdate{Image: &defaultImage}); err != nil {
		writeError(writer, http.StatusInternalServerError, err)
		return
	}
	if err := removeTemporary(filepath.Join(s.artworkDir, filename)); err != nil {
		if rollbackErr := s.writer.UpdateGame(request.Context(), gameID, core.GameUpdate{Image: &game.Image}); rollbackErr != nil {
			slog.Error("restore custom artwork after cleanup failure", "game_id", gameID, "error", rollbackErr)
		}
		writeError(writer, http.StatusInternalServerError, fmt.Errorf("remove custom artwork: %w", err))
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func defaultGameImage(game core.Game) string {
	if game.Store == "steam" && game.StoreID != "" {
		return fmt.Sprintf("https://shared.cloudflare.steamstatic.com/store_item_assets/steam/apps/%s/library_600x900_2x.jpg", game.StoreID)
	}
	return ""
}

func localArtworkFilename(gameID, image string) (string, bool) {
	filename := strings.TrimPrefix(image, "/artwork/")
	if filename == image {
		return "", false
	}
	for _, extension := range []string{".jpg", ".png", ".gif", ".webp"} {
		if filename == gameID+extension {
			return filename, true
		}
	}
	return "", false
}

func (s *Server) cleanupObsoleteArtwork(gameID, keep string) {
	for _, extension := range []string{".jpg", ".png", ".gif", ".webp"} {
		filename := gameID + extension
		if filename == keep {
			continue
		}
		if err := removeTemporary(filepath.Join(s.artworkDir, filename)); err != nil {
			slog.Warn("remove obsolete custom artwork", "game_id", gameID, "file", filename, "error", err)
		}
	}
}

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func removeTemporary(path string) error {
	//nolint:gosec // Callers pass application-owned temporary paths or validated artwork filenames under the private artwork directory.
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
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		slog.Error("write HTTP response", "error", err)
	}
}

func writeError(writer http.ResponseWriter, status int, err error) {
	slog.Error("HTTP request failed", "status", status, "error", err)
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

func localRequestsOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !loopbackHost(request.Host) || !localOrigin(request.Header.Get("Origin")) {
			http.Error(writer, "SaveKnot only accepts requests from its local UI", http.StatusForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func localOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && loopbackHost(parsed.Host)
}

func loopbackHost(hostPort string) bool {
	host := hostPort
	if parsed, _, err := net.SplitHostPort(hostPort); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
