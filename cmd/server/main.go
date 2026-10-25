package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"unour/game"
	"unour/internal/account"
	"unour/internal/admin"
	"unour/internal/bot"
	"unour/internal/botrunner"
	"unour/internal/httpapi"
	"unour/internal/room"
	"unour/internal/store/sqlite"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	cfg, err := loadConfig(os.Getenv)
	if err == nil {
		err = run(ctx, cfg, logger)
	}
	if err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg config, logger *slog.Logger) (runErr error) {
	var adminSessions *admin.Service
	if cfg.adminConfig != "" {
		adminCfg, err := admin.LoadConfig(cfg.adminConfig)
		if err != nil {
			return err
		}
		adminSessions, err = admin.New(adminCfg, nil)
		if err != nil {
			return err
		}
	}
	registry := game.NewRegistry()
	if cfg.rulesDir != "" {
		loaded, err := game.LoadRuleRegistry(os.DirFS(cfg.rulesDir))
		if err != nil {
			return fmt.Errorf("load rule directory: %w", err)
		}
		registry = loaded
	}
	store, err := sqlite.Open(sqlite.Config{Path: cfg.dbPath, MaxRooms: cfg.maxRooms, Codec: room.NewCodec(nil)})
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, store.Close()) }()
	rooms, err := room.New(store, room.Config{Registry: registry, TTL: cfg.roomTTL})
	if err != nil {
		return err
	}
	accounts, err := account.New(store, account.Config{})
	if err != nil {
		return err
	}
	computers, err := botrunner.New(store, rooms, bot.New(registry), botrunner.Config{Logger: logger})
	if err != nil {
		return err
	}
	apiConfig := httpapi.Config{Accounts: accounts, SecureCookies: cfg.secureCookies, Logger: logger, AllowedOrigins: cfg.origins, TrustedProxies: cfg.trustedProxies}
	if adminSessions != nil {
		apiConfig.Admin = &httpapi.Administration{Sessions: adminSessions, Users: account.NewManager(store), Rooms: room.NewManager(rooms, store), Audit: store}
	}
	handler, err := httpapi.New(rooms, apiConfig)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	listener, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	reloadCtx, cancelReload := context.WithCancel(ctx)
	reloadDone := make(chan struct{})
	reloadSignals := make(chan os.Signal, 1)
	if cfg.rulesDir != "" {
		signal.Notify(reloadSignals, syscall.SIGHUP)
		go func() {
			defer close(reloadDone)
			watchRuleReloads(reloadCtx, reloadSignals, registry, cfg.rulesDir, logger)
		}()
	} else {
		close(reloadDone)
	}
	defer func() { signal.Stop(reloadSignals); cancelReload(); <-reloadDone }()
	botCtx, cancelBots := context.WithCancel(ctx)
	botDone := make(chan struct{})
	go func() { defer close(botDone); _ = computers.Run(botCtx) }()
	defer func() { cancelBots(); <-botDone }()
	cleanupCtx, cancelCleanup := context.WithCancel(ctx)
	cleanupDone := make(chan struct{})
	if cfg.roomTTL > 0 {
		go func() { defer close(cleanupDone); sweepRooms(cleanupCtx, rooms, cfg.roomTTL, logger) }()
	} else {
		close(cleanupDone)
	}
	defer func() { cancelCleanup(); <-cleanupDone }()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	logger.Info("UNO server listening", "address", listener.Addr().String(), "database", cfg.dbPath, "max_rooms", cfg.maxRooms, "room_ttl", cfg.roomTTL)
	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shutdown: %w", err)
		}
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		logger.Info("UNO server stopped")
		return nil
	}
}

func sweepRooms(ctx context.Context, rooms *room.Service, ttl time.Duration, logger *slog.Logger) {
	interval := ttl / 4
	if interval > time.Minute {
		interval = time.Minute
	}
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			n, err := rooms.Sweep(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("room cleanup failed", "error", err)
			} else if n > 0 {
				logger.Info("idle rooms removed", "count", n)
			}
		}
	}
}

func watchRuleReloads(ctx context.Context, signals <-chan os.Signal, registry *game.Registry, directory string, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-signals:
			if err := registry.Reload(os.DirFS(directory)); err != nil {
				logger.Error("rule reload rejected; previous catalog retained", "error", err)
			} else {
				logger.Info("rule catalog reloaded", "revision", registry.Catalog().Revision)
			}
		}
	}
}
