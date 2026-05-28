package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/zen66ten/tcl-batch-runner/internal/config"
	"github.com/zen66ten/tcl-batch-runner/internal/runner"
	"github.com/zen66ten/tcl-batch-runner/internal/web"
)

func main() {
	listen  := flag.String("listen", ":8080", "address to listen on")
	dataDir := flag.String("data-dir", ".", "directory for config.yaml and jobs/")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	passphrase := os.Getenv("BECS_RUNNER_KEY")
	if passphrase == "" {
		slog.Warn("BECS_RUNNER_KEY not set; credential encryption/decryption will fail")
	}

	cfg, err := config.Load(filepath.Join(*dataDir, "config.yaml"))
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	run := runner.New(cfg, *dataDir, passphrase)

	app, err := web.NewApp(cfg, run, *dataDir, passphrase)
	if err != nil {
		slog.Error("init web app", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	app.RegisterRoutes(mux)

	slog.Info("becs-runner starting", "listen", *listen, "data-dir", *dataDir)
	if err := http.ListenAndServe(*listen, mux); err != nil {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}
