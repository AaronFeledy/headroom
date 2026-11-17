package opencodego

// OpenCode database contract fixture (opt-in). It checks Headroom's read
// queries against a database created by a real OpenCode CLI, so an upstream
// schema change fails here instead of silently signing users out.
//
// Create a throwaway database and run it from server/:
//
//	T=$(mktemp -d)
//	env -i PATH="$HOME/.opencode/bin:/usr/bin:/bin" HOME=$T \
//	  XDG_DATA_HOME=$T/data XDG_CONFIG_HOME=$T/config \
//	  XDG_CACHE_HOME=$T/cache XDG_STATE_HOME=$T/state \
//	  timeout 120 opencode --pure session list
//	HEADROOM_OPENCODE_DB=$T/data/opencode/opencode.db \
//	  go test -run TestOpenCodeDatabaseContract -count=1 -v ./internal/providers/opencodego/
//
// CI runs it weekly and on pull requests that touch this package against
// OpenCode's `latest` and `dev` npm releases. The database is only ever opened
// read-only.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenCodeDatabaseContract(t *testing.T) {
	path := os.Getenv("HEADROOM_OPENCODE_DB")
	if path == "" {
		t.Skip("set HEADROOM_OPENCODE_DB=<database created by a real OpenCode CLI> to run the schema contract")
	}
	database, err := sql.Open("sqlite", readOnlyDSN(filepath.ToSlash(path)))
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })

	// Every column Headroom's read queries name, table by table.
	required := []struct {
		table   string
		columns []string
	}{
		{"credential", []string{"id", "integration_id", "value", "active", "time_created", "time_updated"}},
		{"migration", []string{"id", "time_completed"}},
		{"account", []string{"id", "url", "access_token", "token_expiry", "time_updated"}},
		{"account_state", []string{"id", "active_account_id", "active_org_id"}},
	}
	for _, table := range required {
		found, err := tableColumns(database, table.table)
		if err != nil {
			t.Fatalf("table %s: %v", table.table, err)
		}
		for _, column := range table.columns {
			if !found[column] {
				t.Errorf("table %s is missing column %s", table.table, column)
			}
		}
	}

	ctx := context.Background()
	for _, discovered := range []bool{true, false} {
		if _, _, err := readCredentialTable(ctx, path, discovered); err != nil {
			t.Fatalf("readCredentialTable(discovered=%v): %v", discovered, err)
		}
	}

	// A fresh CLI database has no sign-in, so polling reports guidance and
	// makes no upstream request.
	server := newUsageServer(t, 0, `{"usage":{"rolling":{"percent":1}}}`)
	provider := NewProvider(Options{
		CredentialsPath: path,
		Environment:     testEnvironment(t, t.TempDir(), nil),
		UsageURL:        server.url(),
		Now:             func() time.Time { return fixedNow },
	})
	data := fetch(t, provider)
	if data.Auth.State != "signed_out" {
		t.Fatalf("auth = %#v, want signed_out for a fresh database", data.Auth)
	}
	if errorText(data) != notSignedInMessage {
		t.Fatalf("error = %q, want %q", errorText(data), notSignedInMessage)
	}
	if _, _, requests := server.recorded(); requests != 0 {
		t.Fatalf("requests = %d, want none", requests)
	}
}

func tableColumns(database *sql.DB, table string) (map[string]bool, error) {
	result, err := database.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	defer result.Close()
	columns := map[string]bool{}
	for result.Next() {
		var (
			position     int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := result.Scan(&position, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, result.Err()
}
