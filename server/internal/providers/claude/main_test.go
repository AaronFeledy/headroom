package claude

import (
	"os"
	"testing"
)

// realUserHome is HOME before TestMain replaced it, for the opt-in macOS
// Keychain fixture: /usr/bin/security finds the user's keychain list there.
var realUserHome string

// TestMain keeps tests away from the developer's real sign-ins. Native Windows
// discovery would otherwise run wsl.exe and read the real WSL home, and an
// empty Options.HomeDir would read the real user profile. Tests that need
// cross-kernel discovery set Options.WSLHomeDir explicitly.
func TestMain(m *testing.M) {
	realUserHome = os.Getenv("HOME")
	home, err := os.MkdirTemp("", "headroom-claude-home-")
	if err != nil {
		panic(err)
	}
	wslHomeDiscoveryEnabled = false
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, home); err != nil {
			panic(err)
		}
	}
	code := m.Run()
	if err := os.RemoveAll(home); err != nil {
		panic(err)
	}
	os.Exit(code)
}
