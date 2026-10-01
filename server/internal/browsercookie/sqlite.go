// Package browsercookie reads disposable snapshots of local browser databases.
package browsercookie

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

var ErrTooLarge = errors.New("SQLite snapshot exceeds 256 MiB")

func Snapshot(ctx context.Context, path string, query func(*sql.DB) error) (err error) {
	dir, err := os.MkdirTemp("", "headroom-cookie-")
	if err != nil {
		return fmt.Errorf("create snapshot: %w", err)
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, statErr := os.Stat(path + suffix)
		if suffix != "" && errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return fmt.Errorf("stat snapshot: %w", statErr)
		}
		total += info.Size()
		if total > 256<<20 {
			return ErrTooLarge
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		data, readErr := os.ReadFile(path + suffix)
		if readErr != nil {
			return fmt.Errorf("read snapshot: %w", readErr)
		}
		if int64(len(data))+total-info.Size() > 256<<20 {
			return ErrTooLarge
		}
		if err := os.WriteFile(filepath.Join(dir, "cookies.sqlite")+suffix, data, 0o600); err != nil {
			return fmt.Errorf("write snapshot: %w", err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "cookies.sqlite"))
	if err != nil {
		return fmt.Errorf("open snapshot: %w", err)
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	db.SetMaxOpenConns(1)
	return query(db)
}

func AppToken(ctx context.Context, path string) (token string, err error) {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	db, err := sql.Open("sqlite", u.String()+"?mode=ro")
	if err == nil {
		err = db.QueryRowContext(ctx, `SELECT value FROM ItemTable WHERE key = 'cursorAuth/accessToken'`).Scan(&token)
		err = errors.Join(err, db.Close())
		if err == nil || errors.Is(err, sql.ErrNoRows) {
			return token, err
		}
	}
	err = Snapshot(ctx, path, func(db *sql.DB) error {
		return db.QueryRowContext(ctx, `SELECT value FROM ItemTable WHERE key = 'cursorAuth/accessToken'`).Scan(&token)
	})
	return token, err
}
