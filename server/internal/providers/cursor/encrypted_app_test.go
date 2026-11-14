package cursor

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/safestorage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func TestEncryptedAppDiscovery(t *testing.T) {
	for _, scenario := range []string{"linux", "linux-firefox-fallback", "darwin", "windows", "windows-unprotect-failure", "windows-missing-state"} {
		t.Run(scenario, func(t *testing.T) {
			goos := strings.Split(scenario, "-")[0]
			appToken := jwtWithSub("app-fixture", "payload")
			key := bytes.Repeat([]byte{42}, 32)
			block, err := aes.NewCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			gcm, err := cipher.NewGCM(block)
			if err != nil {
				t.Fatal(err)
			}
			nonce := bytes.Repeat([]byte{9}, 12)
			blob := gcm.Seal(append([]byte("v10"), nonce...), nonce, []byte(appToken), nil)
			ciphertext := base64.StdEncoding.EncodeToString(blob)
			encrypted := "cursor-safe-storage:v1:" + ciphertext
			wantToken, wantUser, wantKind, wantStatus := tokenWithExpiry(0), "fixture", "cli", "encrypted"
			if scenario == "windows" {
				wantToken, wantUser, wantKind, wantStatus = appToken, "app-fixture", "app", "signed_in"
			}
			if scenario == "linux-firefox-fallback" {
				wantKind = "browser"
			}
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				wire := r.URL.String() + r.Header.Get("Cookie") + r.Header.Get("Authorization")
				if strings.Contains(wire, "cursor-safe-storage") || strings.Contains(wire, ciphertext) || strings.Contains(wire, string(blob)) {
					t.Error("encrypted app material reached HTTP")
				}
				if r.Header.Get("Cookie") != "WorkosCursorSessionToken="+url.QueryEscape(wantUser+"::"+wantToken) {
					t.Error("selected token was not sent as a session cookie")
				}
				w.WriteHeader(http.StatusServiceUnavailable)
			}))
			t.Cleanup(srv.Close)
			client := newTestClient(t, Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
			client.discovery.Environment.GOOS = goos
			client.discovery.Now = func() time.Time { return time.Unix(1000, 0) }
			client.discovery.Unprotect = func(protected []byte) ([]byte, error) {
				if goos != "windows" {
					t.Error("non-Windows attempted unprotect")
				}
				if scenario == "windows-unprotect-failure" {
					return nil, safestorage.ErrUnprotect
				}
				return bytes.Clone(protected), nil
			}
			client.authPath = writeAuthFile(t, wantToken)
			if scenario == "linux-firefox-fallback" {
				if err := os.Remove(client.authPath); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(client.discovery.Environment.Home, ".mozilla", "firefox", "fixture", "cookies.sqlite")
				browser := fixtureDB(t, path, `CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,expiry INTEGER,lastAccessed INTEGER)`)
				if _, err := browser.Exec(`INSERT INTO moz_cookies VALUES('.cursor.com','WorkosCursorSessionToken',?,2000,1)`, url.QueryEscape("fixture::"+wantToken)); err != nil {
					t.Fatal(err)
				}
			}
			app := client.discovery.files(client.authPath)[1]
			db := fixtureDB(t, app.path, `CREATE TABLE ItemTable(key TEXT, value TEXT)`)
			if _, err := db.Exec(`INSERT INTO ItemTable VALUES('cursorAuth/accessToken',?)`, encrypted); err != nil {
				t.Fatal(err)
			}
			root := filepath.Dir(filepath.Dir(filepath.Dir(app.path)))
			if scenario != "windows-missing-state" {
				data := `{"os_crypt":{"encrypted_key":"` + base64.StdEncoding.EncodeToString(append([]byte("DPAPI"), key...)) + `"}}`
				if err := os.WriteFile(filepath.Join(root, "Local State"), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			read := client.discovery.readCredential(context.Background(), app)
			if read.status != wantStatus {
				t.Fatalf("app status=%s", read.status)
			}
			if wantStatus == "encrypted" && (read.failure == nil || read.failure.Kind != usage.FailureOther || read.cookie != "") {
				t.Fatal("encrypted candidate must have a failure and no cookie")
			}
			if scenario == "windows" {
				if err := os.Remove(client.authPath); err != nil {
					t.Fatal(err)
				}
			}
			data, err := client.Fetch(context.Background())
			if err != nil || data.Auth.Source == nil || data.Auth.Source.Kind != wantKind || calls != 1 {
				t.Fatalf("selection failed: calls=%d err=%v auth=%#v", calls, err, data.Auth)
			}
			found := false
			for _, checked := range data.Auth.Checked {
				if checked.Kind == "app" && checked.Status == wantStatus {
					found = true
				}
			}
			if !found {
				t.Fatal("app checked status missing")
			}
		})
	}
}
