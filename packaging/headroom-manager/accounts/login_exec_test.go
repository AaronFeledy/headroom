package accounts

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

const syntheticCredentials = `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh","expiresAt":4102444800000}}`

func TestNativeLoginWithFileProbe(t *testing.T) {
	testNativeLogin(t, func(_ context.Context, dir string) accountconfig.SignIn {
		if strings.TrimSpace(contents(t, filepath.Join(dir, ".credentials.json"))) != syntheticCredentials {
			t.Fatal("login did not save the synthetic credentials")
		}
		return accountconfig.SignIn{State: "signed_in"}
	})
}

func testNativeLogin(t *testing.T, probe func(context.Context, string) accountconfig.SignIn) {
	t.Helper()
	o, _, path := fixture(t)
	a := savedAccount(t, o, path)
	before := contents(t, path)
	bin := filepath.Join(filepath.Dir(path), "fake bin")
	record := filepath.Join(filepath.Dir(path), "login-directory.txt")
	program := filepath.Join(bin, "claude")
	script := "#!/bin/sh\nset -eu\n[ \"$1\" = auth ] && [ \"$2\" = login ]\nprintf '%s' \"$CLAUDE_CONFIG_DIR\" > \"$HEADROOM_LOGIN_RECORD\"\n/bin/mkdir -p \"$CLAUDE_CONFIG_DIR\"\nprintf '%s' '" + syntheticCredentials + "' > \"$CLAUDE_CONFIG_DIR/.credentials.json\"\n"
	if runtime.GOOS == "windows" {
		program += ".cmd"
		script = "@echo off\r\nif not \"%1\"==\"auth\" exit /b 10\r\nif not \"%2\"==\"login\" exit /b 11\r\n>\"%HEADROOM_LOGIN_RECORD%\" <nul set /p \"=%CLAUDE_CONFIG_DIR%\"\r\nif not exist \"%CLAUDE_CONFIG_DIR%\" mkdir \"%CLAUDE_CONFIG_DIR%\"\r\n>\"%CLAUDE_CONFIG_DIR%.credentials.json\" echo " + syntheticCredentials + "\r\nexit /b 0\r\n"
	}
	write(t, program, script)
	if err := os.Chmod(program, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	o.Env = append(o.Env, "PATH="+os.Getenv("PATH"), "HEADROOM_LOGIN_RECORD="+record, "CLAUDE_CONFIG_DIR=wrong")
	if runtime.GOOS == "windows" {
		o.Env = append(o.Env, "SystemRoot="+os.Getenv("SystemRoot"), "ComSpec="+os.Getenv("ComSpec"))
	}
	o.Login, o.SignIn = nil, probe
	o.Args = []string{"login", "claude", a.ID, "--config", path}

	if err := Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}

	if got := contents(t, record); got != a.ConfigDir {
		t.Fatalf("child CLAUDE_CONFIG_DIR = %q, stored = %q", got, a.ConfigDir)
	}
	if contents(t, path) != before {
		t.Fatal("login rewrote config")
	}
}
