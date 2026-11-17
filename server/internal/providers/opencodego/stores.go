package opencodego

import (
	"fmt"
	"io"
	"os"
	"time"
)

const sqliteHeader = "SQLite format 3\x00"

func loadLegacyFile(path string, suffix string) credentialResult {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return credentialResult{kind: credentialKey, suffix: suffix, err: errCredentialMissing}
		}
		return credentialResult{kind: credentialKey, suffix: suffix, err: unreadable(err)}
	}
	token, err := decodeLegacyAuth(data)
	if err != nil {
		return credentialResult{kind: credentialKey, suffix: suffix, err: err}
	}
	return credentialResult{credential: credential{token: token}, kind: credentialKey, suffix: suffix}
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

func (p *Provider) evaluateRows(rows []credentialRow, suffix string, now time.Time) credentialResult {
	if len(rows) == 0 {
		return credentialResult{kind: credentialKey, suffix: suffix, err: errCredentialMissing}
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
		return credentialResult{kind: storedKind(first.value), suffix: suffix, err: errCredentialAmbiguous}
	}
	creds, decodedKind, err := decodeCredentialValue(first.value, now, p.usageURL)
	if err != nil {
		return credentialResult{kind: decodedKind, suffix: suffix, err: err}
	}
	return credentialResult{
		credential: creds,
		kind:       decodedKind,
		suffix:     suffix,
	}
}

// evaluateConsole accepts the CLI's console sign-in only from the provider's own
// origin, and after V2 claims the database only when the CLI refreshed it since.
func (p *Provider) evaluateConsole(table credentialTable, suffix string, now time.Time) credentialResult {
	console := func(err error) credentialResult {
		return credentialResult{kind: credentialConsoleAccount, suffix: suffix, err: err}
	}
	account := table.console
	if account == nil {
		return console(errCredentialMissing)
	}
	if table.claimed && account.updatedMS <= table.claimedAt {
		return console(errCredentialMissing)
	}
	if !sameOrigin(account.url, p.usageURL) {
		result := console(errCredentialMissing)
		result.guidance = &consoleOriginError{rawURL: account.url}
		return result
	}
	if !validToken(account.token) {
		return console(errCredentialInvalid)
	}
	if account.orgID == "" {
		result := console(errCredentialMissing)
		result.guidance = errConsoleNoWorkspace
		return result
	}
	creds := credential{token: account.token, orgID: account.orgID}
	if account.expiryMS != nil {
		creds.expiresAt = time.UnixMilli(*account.expiryMS).UTC()
		if !now.Before(creds.expiresAt) {
			return console(errCredentialExpired)
		}
	}
	return credentialResult{
		credential: creds,
		kind:       credentialConsoleAccount,
		suffix:     suffix,
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func unreadable(err error) error {
	return fmt.Errorf("%w: %w", errCredentialUnreadable, err)
}
