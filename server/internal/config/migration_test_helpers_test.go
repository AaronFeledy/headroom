package config_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

const legacyYAML = "listen_addr: 127.0.0.1:7123\npoll_interval: 37s\n"
const nextYAML = "listen_addr: 127.0.0.1:7456\n"

func writeMigrationFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireMigrationFile(t *testing.T, path, content string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != content {
		t.Fatalf("file %s differs: err=%v", path, err)
	}
}

func requireMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s should not exist: %v", path, err)
	}
}

func requireMigrationLoad(t *testing.T, opts config.LoadOptions, goos, want string) {
	t.Helper()
	cfg, err := config.LoadForOS(context.Background(), opts, goos)
	if err != nil || cfg.ListenAddr != want {
		t.Fatalf("ListenAddr=%q, err=%v, want %q", cfg.ListenAddr, err, want)
	}
}

func migrationLogger() (*slog.Logger, *bytes.Buffer) {
	buffer := new(bytes.Buffer)
	return slog.New(slog.NewTextHandler(buffer, nil)), buffer
}

func requireLogLevels(t *testing.T, logs *bytes.Buffer, want ...slog.Level) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
	if logs.Len() == 0 {
		lines = nil
	}
	if len(lines) != len(want) {
		t.Fatalf("log count=%d, want %d: %s", len(lines), len(want), logs)
	}
	for i, level := range want {
		if !bytes.Contains(lines[i], []byte("level="+level.String())) {
			t.Fatalf("log %d has wrong level: %s", i, lines[i])
		}
	}
}
