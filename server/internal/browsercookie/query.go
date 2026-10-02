package browsercookie

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"time"
)

type Result struct {
	Cookie string
	Status string
}

const names = `('WorkosCursorSessionToken','__Secure-next-auth.session-token','next-auth.session-token')`
const firefoxHosts = `(host IN ('cursor.com','cursor.sh') OR host LIKE '%.cursor.com' OR host LIKE '%.cursor.sh')`
const chromeHosts = `(host_key IN ('cursor.com','cursor.sh') OR host_key LIKE '%.cursor.com' OR host_key LIKE '%.cursor.sh')`

func Read(ctx context.Context, path string, firefox bool, now time.Time) Result {
	result := Result{Status: "signed_out"}
	err := Snapshot(ctx, path, func(db *sql.DB) (queryErr error) {
		query := `SELECT name, value, expiry FROM moz_cookies WHERE ` + firefoxHosts + ` AND name IN ` + names + ` ORDER BY length(host) DESC, expiry DESC, lastAccessed DESC`
		if !firefox {
			query = `SELECT name, '', expires_utc FROM cookies WHERE ` + chromeHosts + ` AND name IN ` + names + ` ORDER BY length(host_key) DESC, expires_utc DESC`
		}
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return err
		}
		defer func() { queryErr = errors.Join(queryErr, rows.Close()) }()
		seen := map[string]bool{}
		var cookies []string
		for rows.Next() {
			var name, value string
			var expiry int64
			if err := rows.Scan(&name, &value, &expiry); err != nil {
				return err
			}
			if !firefox {
				expiry = expiry/1000000 - 11644473600
			}
			if expiry <= now.Unix() {
				if result.Status == "signed_out" {
					result.Status = "expired"
				}
				continue
			}
			if !firefox {
				result.Status = "encrypted"
				continue
			}
			if !seen[name] && value != "" {
				cookies = append(cookies, name+"="+value)
				seen[name] = true
				result.Status = "signed_in"
			}
		}
		result.Cookie = strings.Join(cookies, "; ")
		return rows.Err()
	})
	if err != nil {
		result.Status = "unreadable"
		message := strings.ToLower(err.Error())
		if errors.Is(err, os.ErrPermission) || strings.Contains(message, "locked") || strings.Contains(message, "busy") || strings.Contains(message, "sharing") || strings.Contains(message, "used by another process") {
			result.Status = "locked"
		}
	}
	return result
}
