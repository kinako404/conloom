package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
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

// Serve runs the core service on cfg.Server.Listen, and the web panel on
// cfg.Panel.Listen when that is set, until ctx is done, then shuts the HTTP
// servers down gracefully: in-flight requests are cancelled (long polls
// answer with no actions) and given up to 10s to finish. Unfinished tasks are
// recovered before any request is accepted. If either server stops on its
// own, the other is shut down too and Serve returns the error.
func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger, opts Options) (err error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	a, err := New(cfg, logger, opts)
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
	var panelLn net.Listener
	if a.PanelHandler() != nil {
		if panelLn, err = net.Listen("tcp", cfg.Panel.Listen); err != nil {
			_ = ln.Close()
			return fmt.Errorf("panel: %w", err)
		}
	}
	if err := a.Start(ctx); err != nil {
		_ = ln.Close()
		if panelLn != nil {
			_ = panelLn.Close()
		}
		return err
	}
	// Request contexts are cancelled once shutdown begins, so outbox long
	// polls return at once instead of holding shutdown for their full wait.
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	newServer := func(h http.Handler) *http.Server {
		srv := &http.Server{
			Handler:           h,
			ReadHeaderTimeout: readHeaderTimeout,
			WriteTimeout:      writeTimeout,
			IdleTimeout:       idleTimeout,
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
			BaseContext:       func(net.Listener) context.Context { return baseCtx },
		}
		srv.RegisterOnShutdown(cancelBase)
		return srv
	}
	servers := []*http.Server{newServer(a.Handler())}
	listeners := []net.Listener{ln}
	if panelLn != nil {
		servers = append(servers, newServer(a.PanelHandler()))
		listeners = append(listeners, panelLn)
	}
	served := make(chan error, len(servers))
	for i, srv := range servers {
		go func() { served <- srv.Serve(listeners[i]) }()
	}
	logger.Info("listening", "addr", ln.Addr().String())
	if panelLn != nil {
		logger.Info("panel listening", "addr", panelLn.Addr().String())
	}

	pending := len(servers)
	var errs []error
	select {
	case err := <-served:
		pending--
		errs = append(errs, err)
	case <-ctx.Done():
	}
	logger.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Go(func() {
			if err := srv.Shutdown(sctx); err != nil {
				_ = srv.Close()
				logger.Warn("graceful shutdown incomplete; closed remaining connections", "error", err)
			}
		})
	}
	wg.Wait()
	for ; pending > 0; pending-- {
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
