package opencodego

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/browsercookie"

	_ "modernc.org/sqlite"
)

// credentialSelectionQuery mirrors the CLI's own selection: rows for the
// provider-specific integration first, then active DESC, time_created DESC, and
// id DESC. NULL active flags remain eligible after explicitly flagged rows.
const credentialSelectionQuery = `SELECT integration_id, value, COALESCE(active, 0), time_updated FROM credential WHERE integration_id IN ('opencode-go', 'opencode') ORDER BY CASE integration_id WHEN 'opencode-go' THEN 0 ELSE 1 END, active DESC, time_created DESC, id DESC`

const legacyImportMigration = "20260805200742_import_legacy_credentials"

type credentialRow struct {
	integration string
	value       string
	active      bool
	updatedMS   int64
}

// credentialTable keeps V2's import boundary even after its last row is deleted.
type credentialTable struct {
	rows      []credentialRow
	claimed   bool
	claimedAt int64
}

// readCredentialTable reads the live database read-only first and falls back to
// a disposable snapshot including WAL sidecars. It never opens the live file
// read-write and never modifies rows.
func readCredentialTable(ctx context.Context, path string, discovered bool) (credentialTable, bool, error) {
	table, hasTable, err := queryCredentialTable(ctx, path, withDatabase, discovered)
	if err == nil {
		return table, hasTable, nil
	}
	snapshotTable, snapshotHasTable, snapshotErr := queryCredentialTable(ctx, path, browsercookie.Snapshot, discovered)
	if snapshotErr != nil {
		return credentialTable{}, false, errors.Join(err, snapshotErr)
	}
	return snapshotTable, snapshotHasTable, nil
}

func queryCredentialTable(ctx context.Context, path string, open func(context.Context, string, func(*sql.DB) error) error, discovered bool) (credentialTable, bool, error) {
	var table credentialTable
	var hasTable bool
	err := open(ctx, path, func(database *sql.DB) error {
		var loadErr error
		table, hasTable, loadErr = loadCredentialTable(ctx, database, discovered)
		return loadErr
	})
	if err != nil {
		return credentialTable{}, false, err
	}
	return table, hasTable, nil
}

// readOnlyDSN builds the read-only SQLite URI for a slash-form path. SQLite's
// documented Windows form is file:///C:/path, so a path without a leading slash
// grows one; Unix paths and UNC shares already start with a slash and keep the
// URI they had.
func readOnlyDSN(slashPath string) string {
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	location := url.URL{Scheme: "file", Path: slashPath}
	return location.String() + "?mode=ro"
}

func withDatabase(ctx context.Context, path string, query func(*sql.DB) error) (err error) {
	database, err := sql.Open("sqlite", readOnlyDSN(filepath.ToSlash(path)))
	if err != nil {
		return err
	}
	database.SetMaxOpenConns(1)
	defer func() { err = errors.Join(err, database.Close()) }()
	return query(database)
}

// loadCredentialTable ignores V1's unused schema during discovery; explicit
// paths retain direct row selection without consulting migration metadata.
func loadCredentialTable(ctx context.Context, database *sql.DB, discovered bool) (credentialTable, bool, error) {
	var table credentialTable
	var marker int
	if discovered {
		err := database.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'migration'`).Scan(&marker)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return table, false, err
		}
		if err == nil {
			err = database.QueryRowContext(ctx, `SELECT time_completed FROM migration WHERE id = ?`, legacyImportMigration).Scan(&table.claimedAt)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return table, false, err
			}
			table.claimed = err == nil
		}
		if !table.claimed {
			return table, false, nil
		}
	}
	err := database.QueryRowContext(ctx, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'credential'`).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return table, false, nil
	}
	if err != nil {
		return credentialTable{}, false, err
	}
	result, err := database.QueryContext(ctx, credentialSelectionQuery)
	if err != nil {
		return credentialTable{}, true, err
	}
	defer result.Close()
	for result.Next() {
		var integration sql.NullString
		var value sql.NullString
		var active int
		var updatedMS int64
		if err := result.Scan(&integration, &value, &active, &updatedMS); err != nil {
			return credentialTable{}, true, err
		}
		table.rows = append(table.rows, credentialRow{
			integration: integration.String,
			value:       value.String,
			active:      active != 0,
			updatedMS:   updatedMS,
		})
	}
	if err := result.Err(); err != nil {
		return credentialTable{}, true, err
	}
	return table, true, nil
}
