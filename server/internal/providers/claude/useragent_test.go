package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func writeCLITarget(t *testing.T, home, name string) string {
	t.Helper()
	target := filepath.Join(home, "versions", name)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("not read"), 0o644); err != nil {
		t.Fatal(err)
	}
	return target
}

func linkCLI(t *testing.T, home, target string) {
	t.Helper()
	link := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func Test_UserAgent_uses_newer_installed_cli_version(t *testing.T) {
	home := t.TempDir()
	linkCLI(t, home, writeCLITarget(t, home, "2.1.322"))
	client := New(Options{HomeDir: home})
	if got, want := client.userAgent(), "claude-cli/2.1.322 (external, cli)"; got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}

func Test_UserAgent_accepts_the_pinned_installed_version(t *testing.T) {
	home := t.TempDir()
	linkCLI(t, home, writeCLITarget(t, home, pinnedCLIVersion))
	if got := installedCLIVersion(home); got != pinnedCLIVersion {
		t.Fatalf("version = %q, want %q", got, pinnedCLIVersion)
	}
}

func Test_UserAgent_rejects_an_older_installed_cli_version(t *testing.T) {
	home := t.TempDir()
	linkCLI(t, home, writeCLITarget(t, home, "2.1.300"))
	if got := installedCLIVersion(home); got != "" {
		t.Fatalf("version = %q, want the pinned fallback", got)
	}
}

func Test_UserAgent_rejects_a_non_version_link_target(t *testing.T) {
	home := t.TempDir()
	linkCLI(t, home, writeCLITarget(t, home, "claude"))
	if got := installedCLIVersion(home); got != "" {
		t.Fatalf("version = %q, want the pinned fallback", got)
	}
}

func Test_UserAgent_falls_back_when_the_launcher_is_missing(t *testing.T) {
	home := t.TempDir()
	if got := installedCLIVersion(home); got != "" {
		t.Fatalf("version = %q, want the pinned fallback", got)
	}
}

func Test_UserAgent_ignores_a_regular_launcher_file(t *testing.T) {
	home := t.TempDir()
	link := filepath.Join(home, ".local", "bin", "claude")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(link, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := installedCLIVersion(home); got != "" {
		t.Fatalf("version = %q, want the pinned fallback", got)
	}
}
