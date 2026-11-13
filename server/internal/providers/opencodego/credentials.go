package opencodego

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

const sqliteHeader = "SQLite format 3\x00"

type credential struct {
	token     string
	expiresAt time.Time
}

type credentialResult struct {
	credential credential
	source     *usage.AuthSource
	err        error
}

// loadCredentials discovers and rereads stores on every fetch so login changes
// cannot hide behind unchanged file metadata. Other kernels remain a fallback.
func loadCredentials(ctx context.Context, configured string, environment winprofile.Environment, now time.Time) credentialResult {
	if configured != "" {
		path := expandHome(configured, environment.Home)
		name := "OpenCode Go CLI"
		if strings.HasPrefix(path, `\\wsl.`) {
			name += " (WSL)"
		}
		return loadConfigured(ctx, credentialSource{path: path, name: name}, now)
	}
	if result, ok := loadGroups(ctx, localGroups(environment), now); ok {
		return result
	}
	if result, ok := loadGroups(ctx, crossKernelGroups(environment), now); ok {
		return result
	}
	return credentialResult{err: errCredentialMissing}
}

// loadConfigured honors an explicit store without inferring its CLI generation.
func loadConfigured(ctx context.Context, source credentialSource, now time.Time) credentialResult {
	authSource := &usage.AuthSource{Kind: "cli", Name: source.name}
	kind, err := detectSourceKind(source.path)
	if err != nil {
		return credentialResult{source: authSource, err: unreadable(err)}
	}
	switch kind {
	case credentialSourceJSON:
		return loadLegacyFile(source.path, authSource)
	case credentialSourceSQLite:
		table, hasTable, err := readCredentialTable(ctx, source.path, false)
		if err != nil {
			return credentialResult{source: authSource, err: unreadable(err)}
		}
		if !hasTable {
			return credentialResult{source: authSource, err: errCredentialMissing}
		}
		return evaluateRows(table.rows, authSource, now)
	default:
		return credentialResult{err: errCredentialMissing}
	}
}

func loadGroups(ctx context.Context, groups []credentialGroup, now time.Time) (credentialResult, bool) {
	for _, group := range groups {
		result := loadGroup(ctx, group, now)
		// Only a missing candidate permits discovery in another directory.
		if errors.Is(result.err, errCredentialMissing) {
			continue
		}
		return result, true
	}
	return credentialResult{}, false
}

// loadGroup excludes imported legacy files unless V1 has rewritten them since
// V2 claimed the database, preventing V2 logout from reviving an imported key.
// Live candidates compete by their own timestamps; a winner's error is final.
func loadGroup(ctx context.Context, group credentialGroup, now time.Time) credentialResult {
	authSource := &usage.AuthSource{Kind: "cli", Name: group.name}
	var table credentialTable
	tableLive := false
	if group.databasePath != "" && fileExists(group.databasePath) {
		var hasTable bool
		var err error
		table, hasTable, err = readCredentialTable(ctx, group.databasePath, true)
		if err != nil {
			return credentialResult{source: authSource, err: unreadable(err)}
		}
		tableLive = hasTable && table.claimed && len(table.rows) > 0
	}
	var legacyMillis int64
	legacyLive := false
	if info, err := os.Stat(group.authPath); err == nil && !info.IsDir() {
		legacyMillis = info.ModTime().UnixMilli()
		legacyLive = !table.claimed || legacyMillis > table.claimedAt
	}
	var legacy credentialResult
	if legacyLive {
		legacy = loadLegacyFile(group.authPath, authSource)
		legacyLive = !errors.Is(legacy.err, errCredentialMissing)
	}
	if tableLive && (!legacyLive || table.rows[0].updatedMS >= legacyMillis) {
		return evaluateRows(table.rows, authSource, now)
	}
	if legacyLive {
		return legacy
	}
	return credentialResult{err: errCredentialMissing}
}

func loadLegacyFile(path string, source *usage.AuthSource) credentialResult {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return credentialResult{err: errCredentialMissing}
		}
		return credentialResult{source: source, err: unreadable(err)}
	}
	token, err := decodeLegacyAuth(data)
	if err != nil {
		return credentialResult{source: source, err: err}
	}
	return credentialResult{credential: credential{token: token}, source: source}
}

func detectSourceKind(path string) (credentialSourceKind, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return credentialSourceNone, nil
		}
		return credentialSourceNone, err
	}
	if info.IsDir() {
		return credentialSourceNone, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return credentialSourceNone, err
	}
	defer file.Close()
	header := make([]byte, len(sqliteHeader))
	n, err := io.ReadFull(file, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return credentialSourceNone, err
	}
	if n == len(sqliteHeader) && string(header) == sqliteHeader {
		return credentialSourceSQLite, nil
	}
	return credentialSourceJSON, nil
}

func evaluateRows(rows []credentialRow, source *usage.AuthSource, now time.Time) credentialResult {
	if len(rows) == 0 {
		return credentialResult{err: errCredentialMissing}
	}
	first := rows[0]
	// The CLI never leaves two active rows for one integration; refuse to guess.
	active := 0
	for _, row := range rows {
		if row.integration == first.integration && row.active {
			active++
		}
	}
	if active > 1 {
		return credentialResult{source: source, err: errCredentialAmbiguous}
	}
	creds, err := decodeCredentialValue(first.value, now)
	if err != nil {
		return credentialResult{source: source, err: err}
	}
	return credentialResult{credential: creds, source: source}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func unreadable(err error) error {
	return fmt.Errorf("%w: %w", errCredentialUnreadable, err)
}
