package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/packaging/headroom-manager/accounts"
)

func TestManagedAccountsServiceReadAbsentInstallRoot(t *testing.T) {
	t.Setenv("HEADROOM_INSTALL_ROOT", "")
	_, err := managedAccountsService{}.Read()
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read error = %v, want os.ErrNotExist", err)
	}
}

func TestAccountsDesktop_RestartServer_unavailable_without_install_root(t *testing.T) {
	t.Setenv("HEADROOM_INSTALL_ROOT", "")
	got, err := accountsDesktop{}.RestartServer(context.Background(), "/tmp/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != accounts.DesktopUnavailable {
		t.Fatalf("got %v, want DesktopUnavailable", got)
	}
}

func TestMapDesktopRestartReply(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		runErr  error
		want    accounts.DesktopRestart
		wantErr string
	}{
		{"exit 3 unavailable", "", exitCode(t, 3), accounts.DesktopUnavailable, ""},
		{"exit 1 empty unknown command", "", exitCode(t, 1), accounts.DesktopUnavailable, ""},
		{"empty success body unavailable", "", nil, accounts.DesktopUnavailable, ""},
		{"busy", `{"ok":true,"result":"busy"}`, nil, accounts.DesktopBusy, ""},
		{"not_owner", `{"ok":true,"result":"not_owner"}`, nil, accounts.DesktopNotOwner, ""},
		{"other_config", `{"ok":true,"result":"other_config","config_path":"/other.yaml"}`, nil, accounts.DesktopOtherConfig, ""},
		{"restarted", `{"ok":true,"result":"restarted","config_path":"/cfg.yaml"}`, nil, accounts.DesktopRestarted, ""},
		{"ok false error", `{"ok":false,"error":"server did not become ready"}`, nil, 0, "server did not become ready"},
		{"ok false blank error", `{"ok":false}`, nil, 0, "desktop restart-server failed"},
		{"old desktop unknown command", `{"ok":false,"error":"unknown command"}`, nil, accounts.DesktopUnavailable, ""},
		{"launch failure", "", os.ErrPermission, 0, "desktop restart-server request failed"},
		{"unknown result unavailable", `{"ok":true,"result":"mystery"}`, nil, accounts.DesktopUnavailable, ""},
		{"invalid json", `{not-json`, nil, 0, "invalid restart-server reply"},
		{"trailing junk", `{"ok":true,"result":"busy"}{"extra":1}`, nil, 0, "invalid restart-server reply"},
		{"exit 1 with body still fails", `{"ok":true,"result":"busy"}`, exitCode(t, 1), 0, "desktop restart-server request failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mapDesktopRestartReply([]byte(tt.output), tt.runErr)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
				}
				if got != tt.want {
					t.Fatalf("result = %v, want %v", got, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCallDesktopRestart_bridge_payload_and_mapping(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")

	t.Run("restarted", func(t *testing.T) {
		requestPath := filepath.Join(t.TempDir(), "request.json")
		exe := fakeDesktop(t, requestPath, `{"ok":true,"result":"restarted","config_path":"/cfg.yaml"}`, 0)
		got, err := callDesktopRestart(context.Background(), exe, configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != accounts.DesktopRestarted {
			t.Fatalf("got %v", got)
		}
		req := readJSONObject(t, requestPath)
		if req["schema"] != float64(1) || req["product"] != "Headroom" || req["command"] != "restart-server" {
			t.Fatalf("request = %#v", req)
		}
		if req["config_path"] != configPath {
			t.Fatalf("config_path = %v, want %q", req["config_path"], configPath)
		}
	})

	t.Run("exit 3 unavailable", func(t *testing.T) {
		exe := fakeDesktop(t, filepath.Join(t.TempDir(), "request.json"), "", 3)
		got, err := callDesktopRestart(context.Background(), exe, configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != accounts.DesktopUnavailable {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("ok false", func(t *testing.T) {
		exe := fakeDesktop(t, filepath.Join(t.TempDir(), "request.json"), `{"ok":false,"error":"owned server failed to start"}`, 0)
		_, err := callDesktopRestart(context.Background(), exe, configPath)
		if err == nil || !strings.Contains(err.Error(), "owned server failed to start") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("busy", func(t *testing.T) {
		exe := fakeDesktop(t, filepath.Join(t.TempDir(), "request.json"), `{"ok":true,"result":"busy"}`, 0)
		got, err := callDesktopRestart(context.Background(), exe, configPath)
		if err != nil {
			t.Fatal(err)
		}
		if got != accounts.DesktopBusy {
			t.Fatalf("got %v", got)
		}
	})
}

func TestCallDesktopRestart_cancelled_context(t *testing.T) {
	exe := fakeDesktop(t, filepath.Join(t.TempDir(), "request.json"), `{"ok":true,"result":"restarted"}`, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := callDesktopRestart(ctx, exe, filepath.Join(t.TempDir(), "config.yaml"))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, accounts.ErrDesktopOutcomeUnknown) {
		t.Fatalf("error = %v, want an unknown outcome caused by context.Canceled", err)
	}
}

func exitCode(t *testing.T, code int) error {
	t.Helper()
	var command *exec.Cmd
	if runtime.GOOS == "windows" {
		command = exec.Command("cmd", "/c", fmt.Sprintf("exit %d", code))
	} else {
		command = exec.Command("sh", "-c", fmt.Sprintf("exit %d", code))
	}
	err := command.Run()
	if err == nil {
		t.Fatalf("exit %d produced nil error", code)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != code {
		t.Fatalf("exit error = %v", err)
	}
	return err
}

func fakeDesktop(t *testing.T, requestPath, reply string, exit int) string {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "cfg.json")
	cfg, err := json.Marshal(map[string]any{"request": requestPath, "reply": reply, "exit": exit})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "main.go")
	program := `package main
import (
	"encoding/json"
	"io"
	"os"
)
func main() {
	raw, err := os.ReadFile(os.Args[0] + ".cfg")
	if err != nil {
		os.Exit(99)
	}
	var cfg struct {
		Request string ` + "`json:\"request\"`" + `
		Reply   string ` + "`json:\"reply\"`" + `
		Exit    int    ` + "`json:\"exit\"`" + `
	}
	if json.Unmarshal(raw, &cfg) != nil {
		os.Exit(99)
	}
	if len(os.Args) != 2 || os.Args[1] != "--headroom-cli-request" { os.Exit(98) }
	data, err := io.ReadAll(os.Stdin)
	if err != nil { os.Exit(97) }
	if err := os.WriteFile(cfg.Request, data, 0o600); err != nil { os.Exit(96) }
	if _, err := os.Stdout.WriteString(cfg.Reply); err != nil { os.Exit(95) }
	os.Exit(cfg.Exit)
}
`
	if err := os.WriteFile(src, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "fake-desktop")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	build := exec.Command("go", "build", "-o", exe, src)
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake desktop: %v: %s", err, out)
	}
	if err := os.Rename(cfgPath, exe+".cfg"); err != nil {
		t.Fatal(err)
	}
	return exe
}

func readJSONObject(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("json %q: %v", data, err)
	}
	return value
}
