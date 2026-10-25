package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unour/game"
)

type reloadLog struct{ entries chan string }

func (w reloadLog) Write(data []byte) (int, error) { w.entries <- string(data); return len(data), nil }
func TestRuleReloadWatcherKeepsTheLastValidDirectory(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "custom.json")
	definition := func(title string) []byte {
		return []byte(`{"name":"custom","version":1,"title":"` + title + `","default_enabled":true,"steps":[]}`)
	}
	if err := os.WriteFile(path, definition("旧"), 0600); err != nil {
		t.Fatal(err)
	}
	registry, err := game.LoadRuleRegistry(os.DirFS(directory))
	if err != nil {
		t.Fatal(err)
	}
	before := registry.Catalog().Revision
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	signals := make(chan os.Signal, 1)
	log := reloadLog{make(chan string, 4)}
	go func() {
		defer close(done)
		watchRuleReloads(ctx, signals, registry, directory, slog.New(slog.NewTextHandler(log, nil)))
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("reload goroutine did not stop")
		}
	}()
	reload := func(data []byte) string {
		t.Helper()
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		signals <- syscall.SIGHUP
		select {
		case entry := <-log.entries:
			return entry
		case <-time.After(time.Second):
			t.Fatal("reload did not complete")
			return ""
		}
	}
	if !strings.Contains(reload([]byte(`{"broken":true}`)), "rejected") || registry.Catalog().Revision != before {
		t.Fatal("failed reload changed live catalog")
	}
	if !strings.Contains(reload(definition("新")), "reloaded") || registry.Catalog().Revision == before {
		t.Fatal("valid reload was not published")
	}
}
