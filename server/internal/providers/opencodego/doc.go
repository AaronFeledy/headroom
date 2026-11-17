// Package opencodego reads the OpenCode CLI's own sign-in for OpenCode Go usage.
//
// # Where Headroom looks
//
// The data directory is $XDG_DATA_HOME/opencode when XDG_DATA_HOME is an
// absolute path, otherwise ~/.local/share/opencode. The SQLite database is
// $OPENCODE_DB, an absolute path or a name relative to that directory
// (":memory:" disables it), and defaults to opencode.db. On Windows the provider
// also checks WSL homes; inside WSL it checks Windows profiles. Source names
// carry " (Windows)" or " (WSL)". An explicit
// providers.opencodego.credentials_path replaces discovery; a path starting with
// \\wsl. is named with " (WSL)".
//
// # Store generations
//
// Stable 1.x keeps only an auth.json "opencode-go" entry with "type":"api".
// OpenCode V2 imports that file once and records the migration
// "20260805200742_import_legacy_credentials"; from then on keys live only in
// the credential table, and signing out deletes the row. Both generations can
// share one data directory. During discovery the table counts only when that
// marker exists, and auth.json counts only without it or when written after it,
// so a V2 sign-out never revives an imported key. Credentials are reread on
// every poll, without a cache.
//
// # Key selection
//
// When both stores offer a key, the newer write wins: the row's time_updated
// against the file's modification time, with ties going to the table. A winner
// that is invalid or expired is reported rather than replaced. Rows are chosen
// the way the CLI chooses them: opencode-go before opencode, then
// active DESC, time_created DESC, id DESC, so legacy rows with a NULL flag stay
// eligible. Never filter on active = 1; two active rows for one integration are
// ambiguous and refused. OAuth expires: 0 means the issuer reported no lifetime.
//
// # Console sign-ins
//
// A 1.x console sign-in keeps no key. account_state row 1 names the active
// account, whose access_token is sent with x-opencode-org-id from the non-blank
// active organization. It is used only when that directory has no OpenCode Go
// key, only when account.url shares the usage endpoint's origin, and after the
// import marker only when OpenCode refreshed the account since. V2 OAuth rows,
// and key rows carrying metadata.orgID, are console credentials: they send
// x-opencode-org-id and are not candidates when metadata.server names another
// origin. An explicit database is read without the marker check and falls back
// to its console account when it holds no OpenCode Go rows.
//
// Keys come before the console sign-in because OpenCode 1.x prefers the console
// token only when the console's remote config supplies OpenCode Go, which is not
// stored locally (https://github.com/AaronFeledy/headroom/issues/115).
//
// # Requests and safety
//
// Each poll makes exactly one GET https://opencode.ai/zen/go/v1/usage with the
// bearer token and, for console credentials, the organization header. A
// redirect to another origin is never followed, so the token cannot leave the
// usage origin. Reported percentages are already 0-100 and are never scaled.
// Headroom never writes, refreshes, rotates, or logs the OpenCode sign-in,
// including console tokens; OpenCode owns refresh.
//
// # Reporting
//
// A selected credential has a kind: key (auth.json or a table row of type "key"
// without metadata.orgID), 1.x console account, or V2 console credential. The
// kind names the source ("OpenCode Go CLI" or "OpenCode console", with the
// cross-kernel suffix) and its sign-in command: opencode auth login for a key or
// V2 console credential, and opencode console login for a 1.x console account.
// Expired, rejected, invalid, and ambiguous results name that command.
// Signed-out guidance is situation-specific: nothing found suggests
// `opencode auth login` and choosing OpenCode Go; a console account without a
// workspace suggests `opencode console switch`; a console sign-in for another
// server shows only that server's hostname and suggests adding an API key. An
// unreadable store carries no source or command. auth.checked lists the examined
// key store and, when it produced nothing and the database was read, the console
// store, in discovery order through the selected one, with status signed_in,
// signed_out, expired, or unreadable.
//
// # Verification
//
// contract_test.go checks the read queries against a database a real CLI
// created: HEADROOM_OPENCODE_DB=<path> go test -run TestOpenCodeDatabaseContract
// ./internal/providers/opencodego. The .github/workflows/opencode-contract.yml
// workflow runs it weekly and on pull requests that touch this package against
// OpenCode's latest and dev npm releases.
//
// # Upstream references
//
// In github.com/anomalyco/opencode at v1.19.5 (console preference and account storage):
// packages/opencode/src/config/config.ts lines 492-521,
// packages/opencode/src/provider/provider.ts lines 1834-1835, and
// packages/opencode/src/account/account.ts.
// At v2.3.0 (usage limits and credential storage):
// packages/core/src/usage-limits.ts lines 164-167 and
// packages/core/src/credential.ts.
package opencodego
