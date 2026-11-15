package accounts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func fixture(t *testing.T) (Options, *bytes.Buffer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "config"))
	out := &bytes.Buffer{}
	env := []string{"HOME=" + home, "USERPROFILE=" + home, "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "APPDATA=" + filepath.Join(home, "config")}
	return Options{Env: env, Stdout: out, Stderr: out, Stdin: strings.NewReader(""),
		SignIn: func(context.Context, string) accountconfig.SignIn {
			return accountconfig.SignIn{State: "signed_in", SourceKind: "cli", SourceName: "synthetic credentials"}
		},
		Login: func(context.Context, string, io.Reader, io.Writer, io.Writer, []string) error {
			t.Fatal("unexpected login")
			return nil
		},
	}, out, filepath.Join(home, "server.yaml")
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func contents(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
