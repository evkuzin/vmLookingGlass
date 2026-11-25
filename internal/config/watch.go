package config

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	hash    [sha256.Size]byte
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
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	w := &Watcher{path: abs, hash: sha256.Sum256(data), log: log}
	w.current.Store(c)
	return w, nil
}

// Config returns the most recently loaded config. Safe for concurrent use.
func (w *Watcher) Config() *Config { return w.current.Load() }

// OnReload registers a callback invoked after each successful reload.
// It must be called before Run.
func (w *Watcher) OnReload(f func(*Config)) { w.onReload = f }

// Run watches the config file until ctx is canceled.
//
// The parent directory is watched rather than the file itself: generators
// typically write a temp file and rename it over the target, which replaces
// the inode and would leave a file-level watch bound to the old one. A
// Kubernetes ConfigMap volume swaps a ..data symlink instead, so any event in
// the directory is a candidate and the content decides whether it changed.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer func() { _ = fsw.Close() }()

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
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			// Every event is a wake-up, but only the watched file or
			// kubelet's ..data swap may push a pending reload back: a
			// sibling written more often than the delay would otherwise
			// postpone it forever.
			if timer == nil {
				timer = time.NewTimer(debounceDelay)
			} else if event.Name == w.path ||
				strings.HasPrefix(filepath.Base(event.Name), "..data") {
				timer.Reset(debounceDelay)
			}
			timerC = timer.C

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.log.Error("config watch error", "err", err)

		case <-timerC:
			timer, timerC = nil, nil
			w.reload()
		}
	}
}

// reload parses the file and swaps it in. A parse failure is logged and the
// previous config is retained, so a bad write cannot take the daemon down.
func (w *Watcher) reload() {
	data, err := os.ReadFile(w.path)
	if err != nil {
		w.log.Error("config reload failed, keeping previous config",
			"path", w.path, "err", err)
		return
	}
	// Content, not inode and mtime: a replacement with the same bytes would
	// otherwise reload and log "config applied" for nothing.
	hash := sha256.Sum256(data)
	if hash == w.hash {
		return
	}
	c, err := Parse(data)
	if err != nil {
		w.log.Error("config reload failed, keeping previous config",
			"path", w.path, "err", err)
		return
	}
	// The hash mirrors the applied config, so a repeated bad write is retried.
	w.hash = hash
	w.current.Store(c)
	w.log.Info("config reloaded", "path", w.path, "agents", len(c.Agents))
	if w.onReload != nil {
		w.onReload(c)
	}
}
