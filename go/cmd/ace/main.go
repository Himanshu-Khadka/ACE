// ace syncs canvas into postgres on a schedule and serves it over http.
//
//	ace serve   api + scheduled syncing (what the docker container runs)
//	ace sync    sync once and exit
//	ace api     only the api, no syncing
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // the container image has no timezone files

	"github.com/Himanshu-Khadka/ACE/go/internal/api"
	"github.com/Himanshu-Khadka/ACE/go/internal/canvas"
	"github.com/Himanshu-Khadka/ACE/go/internal/config"
	"github.com/Himanshu-Khadka/ACE/go/internal/mcpserver"
	"github.com/Himanshu-Khadka/ACE/go/internal/service"
	"github.com/Himanshu-Khadka/ACE/go/internal/store"
	"github.com/Himanshu-Khadka/ACE/go/internal/syncer"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	level := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if err := run(cmd, log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(cmd string, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer st.Close()

	newSyncer := func() (*syncer.Syncer, error) {
		if err := cfg.CheckCanvas(); err != nil {
			return nil, err
		}
		return &syncer.Syncer{
			Canvas: canvas.New(cfg.CanvasURL, cfg.CanvasToken), Store: st, DataDir: cfg.DataDir,
			Workers: cfg.Workers, DueSoon: cfg.DueSoon, Log: log,
		}, nil
	}

	switch cmd {
	case "sync":
		sy, err := newSyncer()
		if err != nil {
			return err
		}
		syncer.NewScheduler(sy, 0, cfg.WebhookURL).RunOnce(ctx)
		return nil
	case "serve", "api":
		svc := service.New(st, cfg.Location)
		srv := &api.Server{Svc: svc, Token: cfg.APIToken, DataDir: cfg.DataDir, Log: log,
			MCP: mcpserver.Handler(svc)}
		if cmd == "serve" {
			sy, err := newSyncer()
			if err != nil {
				return err
			}
			srv.Scheduler = syncer.NewScheduler(sy, cfg.SyncInterval, cfg.WebhookURL)
			go srv.Scheduler.Run(ctx)
			log.Info("syncing canvas", "every", cfg.SyncInterval, "url", cfg.CanvasURL)
		}
		if cfg.APIToken == "" {
			log.Warn("API_TOKEN is empty, anyone who can reach the port can read your courses")
		}
		return listen(ctx, cfg.ListenAddr, srv.Handler(), log)
	}
	return fmt.Errorf("unknown command %q (use serve, sync or api)", cmd)
}

func listen(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	hs := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Info("api listening", "addr", addr)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := hs.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
