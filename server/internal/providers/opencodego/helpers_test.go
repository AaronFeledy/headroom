package opencodego

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

var fixedNow = time.Date(2026, 8, 12, 1, 0, 0, 0, time.UTC)

// credentialSchema is the table the current stable CLI creates, byte for byte.
const credentialSchema = "CREATE TABLE `credential` (\n" +
	"  `id` text PRIMARY KEY, `integration_id` text, `label` text NOT NULL, `value` text NOT NULL,\n" +
	"  `connector_id` text, `method_id` text, `active` integer,\n" +
	"  `time_created` integer NOT NULL, `time_updated` integer NOT NULL)"

// credentialFixture is one stored credential row. A nil active flag matches a
// legacy row, which the CLI stores as NULL.
type credentialFixture struct {
	id          string
	integration string
	value       string
	active      *int
	createdMS   int64
	updatedMS   int64
}

type usageServer struct {
	server         *httptest.Server
	mu             sync.Mutex
	authorizations []string
	paths          []string
	accepts        []string
	agents         []string
	requests       int
}

func newUsageServer(t *testing.T, status int, body string) *usageServer {
	t.Helper()
	recorder := &usageServer{}
	recorder.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		recorder.mu.Lock()
		recorder.requests++
		recorder.authorizations = append(recorder.authorizations, request.Header.Get("Authorization"))
		recorder.paths = append(recorder.paths, request.URL.Path)
		recorder.accepts = append(recorder.accepts, request.Header.Get("Accept"))
		recorder.agents = append(recorder.agents, request.Header.Get("User-Agent"))
		recorder.mu.Unlock()
		if status != 0 {
			writer.WriteHeader(status)
		}
		if body != "" {
			_, _ = io.WriteString(writer, body)
		}
	}))
	t.Cleanup(recorder.server.Close)
	return recorder
}

func (s *usageServer) url() string { return s.server.URL + "/zen/go/v1/usage" }

func (s *usageServer) recorded() ([]string, []string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.authorizations...), append([]string(nil), s.paths...), s.requests
}

func (s *usageServer) headers() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.accepts...), append([]string(nil), s.agents...)
}

func testEnvironment(t *testing.T, home string, variables map[string]string) winprofile.Environment {
	t.Helper()
	return winprofile.Environment{
		GOOS: "linux",
		Home: home,
		Root: t.TempDir(),
		Env: func(key string) string {
			return variables[key]
		},
	}
}

func newProvider(t *testing.T, environment winprofile.Environment, usageURL string) *Provider {
	t.Helper()
	return NewProvider(Options{
		Environment: environment,
		UsageURL:    usageURL,
		Now:         func() time.Time { return fixedNow },
	})
}

func localDataDirectory(home string) string {
	return filepath.Join(home, ".local", "share", "opencode")
}

func writeLegacyAuth(t *testing.T, home string, body string) string {
	t.Helper()
	directory := localDataDirectory(home)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "auth.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func createCredentialDatabase(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(credentialSchema); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func claimV2Database(t *testing.T, path string, completedMS int64) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS "migration" (id TEXT PRIMARY KEY, time_completed INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO migration (id, time_completed) VALUES (?, ?)`, "20260805200742_import_legacy_credentials", completedMS); err != nil {
		t.Fatal(err)
	}
}

// createUnrelatedDatabase writes a SQLite file that has no credential table.
func createUnrelatedDatabase(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(`CREATE TABLE unrelated (id INTEGER)`); err != nil {
		t.Fatal(err)
	}
}

func insertCredential(t *testing.T, path string, row credentialFixture) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(
		`INSERT INTO credential (id, integration_id, label, value, active, time_created, time_updated) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.id, row.integration, "fixture", row.value, row.active, row.createdMS, row.updatedMS,
	); err != nil {
		t.Fatal(err)
	}
}

func activeFlag(value int) *int { return &value }

// millisAt converts a fixture-clock offset into epoch milliseconds.
func millisAt(offset time.Duration) int64 { return fixedNow.Add(offset).UnixMilli() }

// setModTime pins a file's modification time, which selects between the two
// stores in one data directory.
func setModTime(t *testing.T, path string, millis int64) {
	t.Helper()
	stamp := time.UnixMilli(millis)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func bumpModTime(t *testing.T, paths ...string) {
	t.Helper()
	stamp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, path := range paths {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
}

func updateCredential(t *testing.T, path string, statement string, args ...any) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(statement, args...); err != nil {
		t.Fatal(err)
	}
}

func fetch(t *testing.T, provider *Provider) usage.UsageData {
	t.Helper()
	data, err := provider.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch returned error: %v", err)
	}
	return data
}

func errorText(data usage.UsageData) string {
	if data.Error == nil {
		return ""
	}
	return *data.Error
}

func bucketByID(data usage.UsageData, id string) (usage.Bucket, bool) {
	for _, bucket := range data.Buckets {
		if bucket.ID == id {
			return bucket, true
		}
	}
	return usage.Bucket{}, false
}
