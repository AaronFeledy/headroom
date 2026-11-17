package opencodego

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type credential struct {
	token     string
	orgID     string
	expiresAt time.Time
}

// Guidance survives missing candidates so a later usable credential still wins.
type credentialResult struct {
	credential credential
	kind       credentialKind
	suffix     string
	guidance   error
	checked    []usage.AuthChecked
	err        error
}

func (r credentialResult) authSource() *usage.AuthSource {
	switch {
	case r.err == nil,
		errors.Is(r.err, errCredentialExpired),
		errors.Is(r.err, errCredentialInvalid),
		errors.Is(r.err, errCredentialAmbiguous):
		return &usage.AuthSource{Kind: "cli", Name: r.kind.sourceName(r.suffix)}
	default:
		return nil
	}
}

func (r credentialResult) signInCommand() *string {
	command := r.kind.signInCommand()
	switch {
	case errors.Is(r.err, errCredentialUnreadable):
		return nil
	case errors.Is(r.guidance, errConsoleNoWorkspace):
		command = consoleSwitchCommand
	case errors.Is(r.err, errCredentialMissing):
		command = signInCommandText
	}
	return &command
}

// loadCredentials discovers and rereads stores on every fetch so login changes
// cannot hide behind unchanged file metadata. Other kernels remain a fallback.
func (p *Provider) loadCredentials(ctx context.Context) credentialResult {
	now := p.now()
	if p.configured != "" {
		path := expandHome(p.configured, p.environment.Home)
		suffix := ""
		if strings.HasPrefix(path, `\\wsl.`) {
			suffix = " (WSL)"
		}
		return p.loadConfigured(ctx, credentialSource{path: path, suffix: suffix}, now)
	}
	local := p.loadGroups(ctx, localGroups(p.environment), now, credentialResult{})
	if !errors.Is(local.err, errCredentialMissing) {
		return local
	}
	return p.loadGroups(ctx, crossKernelGroups(p.environment), now, local)
}

// loadConfigured honors an explicit store without inferring its CLI generation.
func (p *Provider) loadConfigured(ctx context.Context, source credentialSource, now time.Time) credentialResult {
	unreadableStore := func(err error) credentialResult {
		result := credentialResult{kind: credentialKey, suffix: source.suffix, err: unreadable(err)}
		result.checked = []usage.AuthChecked{keyCheck(source.suffix, result)}
		return result
	}
	kind, err := detectSourceKind(source.path)
	if err != nil {
		return unreadableStore(err)
	}
	switch kind {
	case credentialSourceJSON:
		result := loadLegacyFile(source.path, source.suffix)
		result.checked = []usage.AuthChecked{keyCheck(source.suffix, result)}
		return result
	case credentialSourceSQLite:
		table, _, err := readCredentialTable(ctx, source.path, false)
		if err != nil {
			return unreadableStore(err)
		}
		// Rows win; a database with no OpenCode Go rows may still hold the
		// CLI's console sign-in, read here without the migration check.
		if len(table.rows) > 0 {
			result := p.evaluateRows(table.rows, source.suffix, now)
			result.checked = []usage.AuthChecked{keyCheck(source.suffix, result)}
			return result
		}
		console := p.evaluateConsole(table, source.suffix, now)
		console.checked = []usage.AuthChecked{
			keyCheck(source.suffix, credentialResult{err: errCredentialMissing}),
			consoleCheck(source.suffix, console.err),
		}
		return console
	default:
		result := credentialResult{kind: credentialKey, suffix: source.suffix, err: errCredentialMissing}
		result.checked = []usage.AuthChecked{keyCheck(source.suffix, result)}
		return result
	}
}

func (p *Provider) loadGroups(ctx context.Context, groups []credentialGroup, now time.Time, previous credentialResult) credentialResult {
	checked, guidance := previous.checked, previous.guidance
	for _, group := range groups {
		result := p.loadGroup(ctx, group, now, checked)
		checked = result.checked
		if guidance == nil {
			guidance = result.guidance
		}
		// Only a missing candidate permits discovery in another directory.
		if errors.Is(result.err, errCredentialMissing) {
			continue
		}
		return result
	}
	return credentialResult{kind: credentialKey, err: errCredentialMissing, checked: checked, guidance: guidance}
}

// loadGroup excludes imported legacy files unless V1 has rewritten them since
// V2 claimed the database, preventing V2 logout from reviving an imported key.
// Live candidates compete by their own timestamps; a winner's error is final.
// The console sign-in is the directory's last candidate, after both keys.
func (p *Provider) loadGroup(ctx context.Context, group credentialGroup, now time.Time, checked []usage.AuthChecked) credentialResult {
	var table credentialTable
	tableLive := false
	databaseRead := false
	if group.databasePath != "" && fileExists(group.databasePath) {
		var hasTable bool
		var err error
		table, hasTable, err = readCredentialTable(ctx, group.databasePath, true)
		if err != nil {
			return credentialResult{
				kind:    credentialKey,
				suffix:  group.suffix,
				err:     unreadable(err),
				checked: append(checked, usage.AuthChecked{Kind: "cli", Name: credentialKey.sourceName(group.suffix), Status: "unreadable"}),
			}
		}
		databaseRead = true
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
		legacy = loadLegacyFile(group.authPath, group.suffix)
		legacyLive = !errors.Is(legacy.err, errCredentialMissing)
	}
	var result credentialResult
	switch {
	case tableLive && (!legacyLive || table.rows[0].updatedMS >= legacyMillis):
		result = p.evaluateRows(table.rows, group.suffix, now)
	case legacyLive:
		result = legacy
	default:
		result = credentialResult{kind: credentialKey, suffix: group.suffix, err: errCredentialMissing}
	}
	checked = append(checked, keyCheck(group.suffix, result))
	if !errors.Is(result.err, errCredentialMissing) || !databaseRead {
		result.checked = checked
		return result
	}
	// Keys come first: OpenCode 1.x prefers the console token over an API key
	// only when the console's remote config supplies OpenCode Go, and that
	// config is not stored locally. See https://github.com/AaronFeledy/headroom/issues/115.
	console := p.evaluateConsole(table, group.suffix, now)
	console.checked = append(checked, consoleCheck(group.suffix, console.err))
	return console
}

// keyCheck names the key stores by the kind they produced; a store that held
// nothing is still named as the CLI key store.
func keyCheck(suffix string, result credentialResult) usage.AuthChecked {
	kind := result.kind
	if errors.Is(result.err, errCredentialMissing) {
		kind = credentialKey
	}
	return usage.AuthChecked{Kind: "cli", Name: kind.sourceName(suffix), Status: checkedStatus(result.err)}
}

func consoleCheck(suffix string, err error) usage.AuthChecked {
	return usage.AuthChecked{Kind: "cli", Name: credentialConsoleAccount.sourceName(suffix), Status: checkedStatus(err)}
}
