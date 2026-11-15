package accountconfig

import (
	"context"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/claude"
)

// Checked identifies an examined local credential source and its sign-in status.
type Checked struct{ Kind, Name, Status string }

// SignIn reports local discovery: signed_in, signed_out, expired, locked, or unreadable.
// SourceKind and SourceName are empty when no source was selected.
type SignIn struct {
	State, SourceKind, SourceName string
	Checked                       []Checked
}

// ClaudeSignIn probes only this Claude config directory using the system read-only Keychain adapter.
// It never refreshes credentials, writes them, or contacts a provider.
func ClaudeSignIn(ctx context.Context, configDir string) SignIn {
	dir, err := ExpandConfigDir(configDir)
	if err != nil {
		return SignIn{State: "unreadable", Checked: []Checked{}}
	}
	probe := claude.New(claude.Options{ConfigDir: dir, Keychain: claude.SystemKeychain()}).ProbeSignIn(ctx)
	result := SignIn{State: probe.State, Checked: make([]Checked, 0, len(probe.Checked))}
	if probe.Source != nil {
		result.SourceKind, result.SourceName = probe.Source.Kind, probe.Source.Name
	}
	for _, entry := range probe.Checked {
		result.Checked = append(result.Checked, Checked{Kind: entry.Kind, Name: entry.Name, Status: entry.Status})
	}
	return result
}
