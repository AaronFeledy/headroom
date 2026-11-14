package claude

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Client_Fetch_sign_in_rows_by_os_and_account(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "darwin"} {
		for _, account := range []string{"default", "extra"} {
			for _, contents := range []string{"missing", "{", `{}`, `{"claudeAiOauth":{"accessToken":"a"}}`} {
				t.Run(goos+"/"+account+"/"+contents, func(t *testing.T) {
					path := filepath.Join(t.TempDir(), ".credentials.json")
					if contents != "missing" {
						if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					opts := Options{CredentialsPath: path}
					if account == "extra" {
						opts.CredentialsPath = ""
						opts.ConfigDir = filepath.Dir(path) + "/"
						opts.Name = "Claude (Work)"
						opts.Account = &usage.Account{ID: "work", Label: "Work"}
					}
					client := New(opts)
					client.goos = goos

					got, err := client.Fetch(context.Background())

					if err != nil {
						t.Fatalf("Fetch: %v", err)
					}
					want := "Saved sign-in could not be read. Sign in again."
					if contents == "missing" {
						want = "Not signed in."
					}
					if got.Error == nil || *got.Error != want || !got.NeedsReauth || got.Auth.State != "signed_out" {
						t.Fatalf("sign-in row = %+v, want %q", got, want)
					}
					if got.ReauthCommand == nil || got.Auth.SignInCommand == nil || *got.ReauthCommand != *got.Auth.SignInCommand {
						t.Fatal("sign-in and reauth commands must match")
					} else if account == "default" && *got.ReauthCommand != "claude auth login" {
						t.Fatalf("default sign-in command = %q", *got.ReauthCommand)
					}
				})
			}
		}
	}
}
