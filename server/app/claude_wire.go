package app

import "github.com/AaronFeledy/claude-usage-widget/server/internal/providers/claude"

// claudeKeychain supplies the OS Keychain adapter for Claude defaults and accounts.
// App tests replace it with a nil factory so no test reaches /usr/bin/security.
var claudeKeychain = claude.SystemKeychain
