package claude

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
)

func Test_Client_default_config_reaches_WSL_and_OpenCode(t *testing.T) {
	for _, goos := range []string{"windows", "linux"} {
		t.Run(goos, func(t *testing.T) {
			// Given
			home := t.TempDir()
			wslHome := t.TempDir()
			openCodePath := writeOpenCodeCredentialsIn(t, filepath.Join(home, ".local", "share", "opencode", "auth.json"), credentialFixture{Access: "opencode", Refresh: "r", ExpiresAt: futureMillis(t)})
			writeClaudeCredentialsIn(t, filepath.Join(wslHome, ".claude", ".credentials.json"), credentialFixture{Access: "wsl", Refresh: "r", ExpiresAt: futureMillis(t)})
			cfg, err := config.Load(context.Background(), config.LoadOptions{Args: []string{"-config", filepath.Join(home, "absent.yaml")}})
			if err != nil {
				t.Fatal(err)
			}
			want := "opencode"
			if goos == "windows" {
				want = "wsl"
			}
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Header.Get("Authorization") != "Bearer "+want || r.Method != http.MethodGet {
					t.Error("default config selected the wrong credential source")
				}
				writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
			}))
			defer server.Close()
			client := New(Options{CredentialsPath: cfg.Providers["claude"].CredentialsPath, HomeDir: home, WSLHomeDir: wslHome, OpenCodePath: openCodePath, HTTPClient: server.Client(), UsageURL: server.URL, TokenURL: server.URL})
			client.goos = goos
			// When
			got, err := client.Fetch(context.Background())
			// Then
			if err != nil || got.Error != nil || got.Auth.State != "signed_in" || requests != 1 {
				t.Fatalf("default discovery failed: %+v, %v", got, err)
			}
		})
	}
}
