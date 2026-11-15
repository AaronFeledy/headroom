package claude

import (
	"os"
	"testing"
)

// TestMain keeps tests away from the developer's real sign-ins. Native Windows
// discovery would otherwise run wsl.exe and read the real WSL home, and an
// empty Options.HomeDir would read the real user profile. Tests that need
// cross-kernel discovery set Options.WSLHomeDir explicitly.
func TestMain(m *testing.M) {
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
