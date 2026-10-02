package browsercookie

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func databaseFixture(t *testing.T, schema string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cookies.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	return path, db
}

func Test_Firefox_snapshot_reads_WAL_and_filters_orders_cookies(t *testing.T) {
	// Given
	path, db := databaseFixture(t, `PRAGMA journal_mode=WAL; CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,expiry INTEGER,lastAccessed INTEGER)`)
	now := time.Unix(1000, 0)
	for _, row := range []struct {
		host, name, value string
		expiry, access    int64
	}{
		{"cursor.com", "WorkosCursorSessionToken", "short", 2000, 5},
		{".cursor.com", "WorkosCursorSessionToken", "preferred", 2000, 10},
		{".cursor.com", "next-auth.session-token", "second", 2000, 20},
		{"evilcursor.com", "WorkosCursorSessionToken", "excluded", 5000, 20},
		{".cursor.sh", "__Secure-next-auth.session-token", "expired", 999, 20},
	} {
		if _, err := db.Exec(`INSERT INTO moz_cookies VALUES(?,?,?,?,?)`, row.host, row.name, row.value, row.expiry, row.access); err != nil {
			t.Fatal(err)
		}
	}
	// When
	result := Read(context.Background(), path, true, now)
	// Then
	if result.Status != "signed_in" || result.Cookie != "next-auth.session-token=second; WorkosCursorSessionToken=preferred" {
		t.Fatalf("result=%#v", result)
	}
}

func Test_Browser_checked_statuses_from_SQLite_rows(t *testing.T) {
	// Given
	for _, tt := range []struct {
		firefox bool
		expiry  int64
		want    string
	}{
		{true, 0, "signed_out"}, {true, 999, "expired"}, {false, 0, "signed_out"}, {false, 999, "expired"}, {false, 2000, "encrypted"},
	} {
		t.Run(tt.want+time.Unix(tt.expiry, 0).String(), func(t *testing.T) {
			schema := `CREATE TABLE cookies(host_key TEXT,name TEXT,encrypted_value BLOB,expires_utc INTEGER)`
			if tt.firefox {
				schema = `CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,expiry INTEGER,lastAccessed INTEGER)`
			}
			path, db := databaseFixture(t, schema)
			if tt.expiry != 0 {
				query := `INSERT INTO cookies VALUES('.cursor.com','WorkosCursorSessionToken',x'763230',?)`
				expiry := (tt.expiry + 11644473600) * 1000000
				if tt.firefox {
					query = `INSERT INTO moz_cookies VALUES('.cursor.com','WorkosCursorSessionToken','fixture',?,1)`
					expiry = tt.expiry
				}
				if _, err := db.Exec(query, expiry); err != nil {
					t.Fatal(err)
				}
			}
			// When
			result := Read(context.Background(), path, tt.firefox, time.Unix(1000, 0))
			// Then
			if result.Status != tt.want || result.Cookie != "" {
				t.Fatal(result)
			}
		})
	}
}

func Test_Firefox_profiles_prefers_installs_then_default_then_alphabetical(t *testing.T) {
	// Given
	root := t.TempDir()
	for _, name := range []string{"aaa", "bbb", "ccc"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "installs.ini"), []byte("[Install]\nDefault=ccc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "profiles.ini"), []byte("[Profile0]\nPath=bbb\nIsRelative=1\nDefault=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// When
	profiles := FirefoxProfiles(root)
	// Then
	if len(profiles) != 3 || filepath.Base(profiles[0]) != "ccc" || filepath.Base(profiles[1]) != "bbb" || filepath.Base(profiles[2]) != "aaa" {
		t.Fatal(profiles)
	}
}

func Test_AppToken_reads_ItemTable(t *testing.T) {
	// Given
	path, db := databaseFixture(t, `CREATE TABLE ItemTable(key TEXT,value TEXT)`)
	if _, err := db.Exec(`INSERT INTO ItemTable VALUES('cursorAuth/accessToken','fixture')`); err != nil {
		t.Fatal(err)
	}
	// When
	token, err := AppToken(context.Background(), path)
	// Then
	if err != nil || token != "fixture" {
		t.Fatalf("token read failed: %v", err)
	}
}

func Test_Snapshot_skips_oversized_database(t *testing.T) {
	// Given
	path := filepath.Join(t.TempDir(), "too-large.sqlite")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(257 << 20); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	// When
	result := Read(context.Background(), path, true, time.Unix(1000, 0))
	// Then
	if result.Status != "unreadable" {
		t.Fatal(result)
	}
}

func Test_Browser_locked_status_for_permission_blocked_fixture(t *testing.T) {
	// Given
	path, _ := databaseFixture(t, `CREATE TABLE moz_cookies(host TEXT,name TEXT,value TEXT,expiry INTEGER,lastAccessed INTEGER)`)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil {
			t.Error(err)
		}
	})
	// When
	result := Read(context.Background(), path, true, time.Unix(1000, 0))
	// Then
	if result.Status != "locked" {
		t.Fatal(result)
	}
}
