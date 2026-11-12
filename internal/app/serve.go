package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/kinako404/conloom/internal/config"
)

const (
	readHeaderTimeout = 10 * time.Second
	// writeTimeout exceeds the 30s maximum outbox long-poll wait.
	writeTimeout    = 45 * time.Second
	idleTimeout     = 120 * time.Second
	shutdownTimeout = 10 * time.Second
)

// Serve runs the core service on cfg.Server.Listen until ctx is done, then
// shuts the HTTP server down gracefully, waiting up to 10s for in-flight
// requests. Unfinished tasks are recovered before any request is accepted.
func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger) (err error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a, err := New(cfg, logger, Options{})
	if err != nil {
		return err
	}
	defer func() {
		if cerr := a.Close(); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}()

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return err
	}
	if err := a.Start(ctx); err != nil {
		_ = ln.Close()
		return err
	}
	srv := &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	logger.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		_ = srv.Close()
		logger.Warn("graceful shutdown incomplete; closed remaining connections", "error", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
