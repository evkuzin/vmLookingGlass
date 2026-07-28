package config

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

// debounceDelay coalesces the burst of events an editor or generator emits
// while rewriting a file, so the config is parsed once per settled change.
const debounceDelay = 250 * time.Millisecond

// Watcher holds the current config and reloads it when the file changes.
// The config is replaced atomically; a failed reload keeps the previous value.
type Watcher struct {
	path    string
	current atomic.Pointer[Config]
	log     *slog.Logger

	// onReload is called after a successful reload, for tests and subscribers.
	onReload func(*Config)
}

// NewWatcher loads the config once and returns a Watcher holding it.
// A config that fails to load is a startup error rather than a warning.
func NewWatcher(path string, log *slog.Logger) (*Watcher, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c, err := Load(abs)
	if err != nil {
		return nil, err
	}
	w := &Watcher{path: abs, log: log}
	w.current.Store(c)
	return w, nil
}

// Config returns the most recently loaded config. Safe for concurrent use.
func (w *Watcher) Config() *Config { return w.current.Load() }

// OnReload registers a callback invoked after each successful reload.
// It must be called before Run.
func (w *Watcher) OnReload(f func(*Config)) { w.onReload = f }

// Run watches the config file until ctx is cancelled.
//
// The parent directory is watched rather than the file itself: generators
// typically write a temp file and rename it over the target, which replaces
// the inode and would leave a file-level watch bound to the old one.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer fsw.Close()

	if err := fsw.Add(filepath.Dir(w.path)); err != nil {
		return err
	}

	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return ctx.Err()

		case event, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if filepath.Clean(event.Name) != w.path {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			if timer == nil {
				timer = time.NewTimer(debounceDelay)
			} else {
				timer.Reset(debounceDelay)
			}
			timerC = timer.C

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Error("config watch error", "err", err)

		case <-timerC:
			timerC = nil
			w.reload()
		}
	}
}

// reload parses the file and swaps it in. A parse failure is logged and the
// previous config is retained, so a bad write cannot take the daemon down.
func (w *Watcher) reload() {
	c, err := Load(w.path)
	if err != nil {
		w.log.Error("config reload failed, keeping previous config",
			"path", w.path, "err", err)
		return
	}
	w.current.Store(c)
	w.log.Info("config reloaded", "path", w.path, "agents", len(c.Agents))
	if w.onReload != nil {
		w.onReload(c)
	}
}
