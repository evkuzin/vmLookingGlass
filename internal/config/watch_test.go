package config

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// writeFile writes via a temp file and rename, the way a config generator would.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// startWatcher runs a Watcher on path and returns it plus a channel of reloads.
func startWatcher(t *testing.T, path string) (*Watcher, chan *Config) {
	t.Helper()
	w, err := NewWatcher(path, discardLogger())
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	reloads := make(chan *Config, 8)
	w.OnReload(func(c *Config) { reloads <- c })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	started := make(chan struct{})
	go func() {
		close(started)
		_ = w.Run(ctx)
	}()
	<-started
	// Give the watcher a moment to register before the test mutates the file.
	time.Sleep(100 * time.Millisecond)
	return w, reloads
}

func awaitReload(t *testing.T, reloads chan *Config) *Config {
	t.Helper()
	select {
	case c := <-reloads:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for reload")
		return nil
	}
}

func TestWatcherReloadsOnRename(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	writeFile(t, path, "agents:\n  - url: a\n")

	w, reloads := startWatcher(t, path)
	if got := len(w.Config().Agents); got != 1 {
		t.Fatalf("initial agents = %d, want 1", got)
	}

	writeFile(t, path, "agents:\n  - url: a\n  - url: b\n")

	c := awaitReload(t, reloads)
	if got := len(c.Agents); got != 2 {
		t.Errorf("reloaded agents = %d, want 2", got)
	}
	if got := len(w.Config().Agents); got != 2 {
		t.Errorf("Config() agents = %d, want 2 after reload", got)
	}
}

func TestWatcherKeepsPreviousConfigOnBadWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	writeFile(t, path, "agents:\n  - url: good\n")

	w, reloads := startWatcher(t, path)

	writeFile(t, path, "agents:\n  - url: ftp://bad\n")
	select {
	case <-reloads:
		t.Fatal("bad config triggered a reload callback")
	case <-time.After(1 * time.Second):
	}

	c := w.Config()
	if len(c.Agents) != 1 || c.Agents[0].Name != "good" {
		t.Errorf("bad write replaced config: %+v", c.Agents)
	}

	// A subsequent good write must still be picked up.
	writeFile(t, path, "agents:\n  - url: recovered\n")
	got := awaitReload(t, reloads)
	if got.Agents[0].Name != "recovered" {
		t.Errorf("Name = %q, want recovered", got.Agents[0].Name)
	}
}

func TestWatcherDebouncesBurst(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	writeFile(t, path, "agents:\n  - url: a\n")

	_, reloads := startWatcher(t, path)

	for i := 0; i < 5; i++ {
		writeFile(t, path, "agents:\n  - url: a\n  - url: b\n")
		time.Sleep(20 * time.Millisecond)
	}
	awaitReload(t, reloads)

	select {
	case <-reloads:
		t.Error("burst of writes produced more than one reload")
	case <-time.After(1 * time.Second):
	}
}

func TestWatcherIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	writeFile(t, path, "agents:\n  - url: a\n")

	_, reloads := startWatcher(t, path)

	writeFile(t, filepath.Join(dir, "unrelated.yml"), "agents: []\n")
	select {
	case <-reloads:
		t.Error("unrelated file in the watched directory triggered a reload")
	case <-time.After(1 * time.Second):
	}
}

func TestNewWatcherFailsOnBadInitialConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yml")
	writeFile(t, path, "agents: []\n")

	if _, err := NewWatcher(path, discardLogger()); err == nil {
		t.Error("expected error for config with no agents")
	}
	if _, err := NewWatcher(filepath.Join(dir, "missing.yml"), discardLogger()); err == nil {
		t.Error("expected error for missing file")
	}
}
