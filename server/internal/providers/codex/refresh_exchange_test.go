package codex

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_doRefresh_persists_and_reloads_after_poll_cancellation(t *testing.T) {
	path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"old-r"}}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cancel()
		if _, err := w.Write([]byte(`{"access_token":"new","refresh_token":"new-r","expires_in":3600}`)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: path, TokenURL: server.URL, HTTPClient: server.Client()})
	stale, err := client.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.doRefresh(ctx, stale)
	if err != nil || result != usage.RefreshSuccess {
		t.Fatalf("doRefresh = %v, %v", result, err)
	}
	current, err := client.store.Current(context.Background())
	if err != nil || current.AccessToken != "new" || current.RefreshToken != "new-r" {
		t.Fatalf("reloaded credentials = %+v, %v", current, err)
	}
	loaded, err := NewCredentialStore(CredentialOptions{Path: path}).Load(context.Background())
	if err != nil || loaded.AccessToken != "new" || loaded.RefreshToken != "new-r" {
		t.Fatalf("persisted credentials = %+v, %v", loaded, err)
	}
}

func Test_CredentialStore_Refresh_preserves_either_token_changed_during_exchange(t *testing.T) {
	for _, changed := range []string{"access_token", "refresh_token"} {
		t.Run(changed, func(t *testing.T) {
			path := writeAuth(t, `{"tokens":{"access_token":"old","refresh_token":"old-r"}}`)
			store := NewCredentialStore(CredentialOptions{Path: path})
			stale, err := store.Load(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			pair := map[string]string{"access_token": "old", "refresh_token": "old-r"}
			pair[changed] = "external"
			err = store.Refresh(context.Background(), stale, func(Credentials) (RefreshedTokens, error) {
				data, err := json.Marshal(map[string]map[string]string{"tokens": pair})
				if err != nil {
					return RefreshedTokens{}, err
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					return RefreshedTokens{}, err
				}
				return RefreshedTokens{AccessToken: "new", RefreshToken: "new-r", RefreshedAt: time.Now()}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			current, err := store.Current(context.Background())
			if err != nil || current.AccessToken != pair["access_token"] || current.RefreshToken != pair["refresh_token"] {
				t.Fatalf("concurrent sign-in replaced: %+v, %v", current, err)
			}
		})
	}
}
