package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/saveknot/saveknot/internal/app"
	"github.com/saveknot/saveknot/internal/config"
	"github.com/saveknot/saveknot/internal/logging"
	"github.com/saveknot/saveknot/internal/platform"
)

func main() {
	os.Exit(run())
}

func run() int {
	dataDir := flag.String("data-dir", "", "directory for local SaveKnot state")
	listen := flag.String("listen", "", "loopback listen address, for example 127.0.0.1:32147")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn, or error")
	flag.Parse()

	if *dataDir == "" {
		resolved, err := config.DefaultDataDir()
		if err != nil {
			slog.Error("resolve data directory", "error", err)
			return 1
		}
		*dataDir = resolved
	}
	logFile, logPath, err := logging.Configure(*dataDir, *logLevel)
	if err != nil {
		slog.Error("configure logging", "error", err)
		return 1
	}
	defer func() {
		if err := logFile.Close(); err != nil {
			slog.Error("close log file", "error", err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	application, err := app.Build(ctx, *dataDir, *listen)
	if err != nil {
		slog.Error("start SaveKnot", "error", err)
		return 1
	}
	slog.Info("SaveKnot is running", "data", *dataDir, "log", logPath)
	if err := platform.RunDesktop(ctx, application.WebURL(), application.Run, stop); err != nil {
		slog.Error("run SaveKnot", "error", err)
		return 1
	}
	return 0
}
