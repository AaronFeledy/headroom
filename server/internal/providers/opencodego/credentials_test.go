package opencodego

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func Test_Provider_Fetch_signed_out_when_no_store_exists(t *testing.T) {
	// Given
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	provider := newProvider(t, testEnvironment(t, t.TempDir(), nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if data.Auth.State != "signed_out" || data.NeedsReauth || data.Auth.Source != nil {
		t.Fatalf("auth = %#v", data.Auth)
	}
	if errorText(data) != "OpenCode Go is not signed in. Run `opencode auth login`." {
		t.Fatalf("error = %q", errorText(data))
	}
	if data.Auth.SignInCommand == nil || *data.Auth.SignInCommand != "opencode auth login" || data.Auth.SignInURL != nil {
		t.Fatalf("sign-in = %#v", data.Auth)
	}
	if _, _, requests := server.recorded(); requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func Test_Provider_Fetch_reads_legacy_auth_json(t *testing.T) {
	// Given
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	writeLegacyAuth(t, home, `{"other-provider":{"type":"api","key":"other"},"opencode-go":{"type":"api","key":"legacy-token"}}`)
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	data := fetch(t, provider)

	// Then
	if data.Error != nil {
		t.Fatalf("UsageData.Error = %q", *data.Error)
	}
	if data.Auth.State != "signed_in" || data.Auth.Source == nil || data.Auth.Source.Name != "OpenCode Go CLI" {
		t.Fatalf("auth = %#v", data.Auth)
	}
	auths, _, _ := server.recorded()
	if len(auths) != 1 || auths[0] != "Bearer legacy-token" {
		t.Fatalf("authorizations = %v", auths)
	}
}

func Test_Provider_Fetch_v2_credentials_take_precedence_over_stale_legacy_auth(t *testing.T) {
	// Given a table written after auth.json, both holding an OpenCode Go entry
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	authPath := writeLegacyAuth(t, home, `{"opencode-go":{"type":"api","key":"stale-legacy"}}`)
	setModTime(t, authPath, millisAt(-2*time.Hour))
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-time.Hour))
	insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"v2-token"}`, active: activeFlag(1), createdMS: millisAt(-time.Hour), updatedMS: millisAt(-time.Hour)})
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When
	fetch(t, provider)

	// Then
	auths, _, _ := server.recorded()
	if len(auths) != 1 || auths[0] != "Bearer v2-token" {
		t.Fatalf("authorizations = %v", auths)
	}
}

func Test_Provider_Fetch_does_not_cache_an_unreadable_error(t *testing.T) {
	// Given a valid V2 store and a context that is already cancelled
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-time.Hour))
	insertCredential(t, database, credentialFixture{id: "go", integration: "opencode-go", value: `{"type":"key","key":"db-token"}`, active: activeFlag(1), createdMS: millisAt(0), updatedMS: millisAt(0)})
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	// When the cancelled fetch fails and the same untouched store is fetched again
	_, firstErr := provider.Fetch(cancelled)
	data := fetch(t, provider)

	// Then the cancellation is not cached: the live fetch reads the credential
	if !errors.Is(firstErr, context.Canceled) {
		t.Fatalf("first fetch error = %v, want context.Canceled", firstErr)
	}
	if data.Auth.State != "signed_in" || len(data.Buckets) != 1 {
		t.Fatalf("auth = %#v buckets = %#v", data.Auth, data.Buckets)
	}
	auths, _, requests := server.recorded()
	if requests != 1 || len(auths) != 1 || auths[0] != "Bearer db-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
}

func Test_Provider_Fetch_reads_wal_only_credentials(t *testing.T) {
	// Given a WAL store whose only credential row lives in the -wal sidecar
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":42}}}`)
	home := t.TempDir()
	database := filepath.Join(localDataDirectory(home), "opencode.db")
	createCredentialDatabase(t, database)
	claimV2Database(t, database, millisAt(-time.Hour))
	writer := startWALWriter(t, database)
	provider := newProvider(t, testEnvironment(t, home, nil), server.url())

	// When the CLI writes and later deletes the row while the writer stays open
	if _, err := writer.ExecContext(context.Background(),
		`INSERT INTO credential (id, integration_id, label, value, active, time_created, time_updated) VALUES (?, ?, ?, ?, NULL, ?, ?)`,
		"go", "opencode-go", "fixture", `{"type":"key","key":"wal-token"}`, millisAt(0), millisAt(0)); err != nil {
		t.Fatal(err)
	}
	assertRowOnlyInWAL(t, database, "wal-token")
	if table, hasTable, err := queryCredentialTable(context.Background(), database, withDatabase, true); err != nil || !hasTable || len(table.rows) != 1 {
		t.Fatalf("read-only rows = %#v error = %v, want the WAL-only row", table.rows, err)
	}
	first := fetch(t, provider)
	if _, err := writer.ExecContext(context.Background(),
		`DELETE FROM credential WHERE id = ?`, "go"); err != nil {
		t.Fatal(err)
	}
	second := fetch(t, provider)

	// Then both committed WAL states are observed without a checkpoint
	if first.Auth.State != "signed_in" || len(first.Buckets) != 1 {
		t.Fatalf("first auth = %#v buckets = %#v", first.Auth, first.Buckets)
	}
	if second.Auth.State != "signed_out" || second.NeedsReauth {
		t.Fatalf("second auth = %#v", second.Auth)
	}
	auths, _, requests := server.recorded()
	if requests != 1 || len(auths) != 1 || auths[0] != "Bearer wal-token" {
		t.Fatalf("requests = %d authorizations = %v", requests, auths)
	}
}

// startWALWriter holds one write connection open in WAL mode with automatic
// checkpointing disabled, so committed rows stay in the -wal sidecar.
func startWALWriter(t *testing.T, path string) *sql.Conn {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	var mode string
	if err := connection.QueryRowContext(context.Background(), `PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
	if _, err := connection.ExecContext(context.Background(), `PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	return connection
}

// assertRowOnlyInWAL proves the fixture matches a live CLI: the committed row
// exists in the -wal sidecar and has not reached the main database file.
func assertRowOnlyInWAL(t *testing.T, path string, value string) {
	t.Helper()
	info, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("stat wal sidecar: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("wal sidecar is empty, want a non-empty -wal")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), value) {
		t.Fatal("credential value reached the main database file")
	}
}
