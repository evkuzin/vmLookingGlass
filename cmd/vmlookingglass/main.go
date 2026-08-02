// Command vmlookingglass aggregates scrape target status from many vmagents
// into a single searchable view.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/evkuzin/vmLookingGlass/internal/api"
	"github.com/evkuzin/vmLookingGlass/internal/config"
	"github.com/evkuzin/vmLookingGlass/internal/daemon"
)

// shutdownTimeout bounds how long in-flight HTTP requests may take to finish.
const shutdownTimeout = 10 * time.Second

func main() {
	configPath := flag.String("config", "/etc/vmlookingglass/config.yml", "path to the config file")
	listen := flag.String("listen", "", "HTTP listen address, overriding the config")
	logLevel := flag.String("log-level", "info", "log level: debug, info, warn or error")
	flag.Parse()

	log, err := newLogger(*logLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	if err := run(*configPath, *listen, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(configPath, listenOverride string, log *slog.Logger) error {
	watcher, err := config.NewWatcher(configPath, log)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	cfg := watcher.Config()

	listen := cfg.Listen
	if listenOverride != "" {
		listen = listenOverride
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	d := daemon.New(log)
	watcher.OnReload(d.Reload)

	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		if err := watcher.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("config watcher stopped", "err", err)
		}
	}()

	daemonDone := make(chan struct{})
	go func() {
		defer close(daemonDone)
		d.Run(ctx, cfg)
	}()

	srv := &http.Server{
		Addr:              listen,
		Handler:           api.New(d.Store(), log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", listen, "agents", len(cfg.Agents), "config", configPath)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
		log.Info("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Warn("http shutdown", "err", err)
	}

	stop()
	<-daemonDone
	<-watchDone
	return nil
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("invalid log level %q", level)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})), nil
}
