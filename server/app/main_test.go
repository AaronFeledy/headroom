package app

import (
	"os"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/providers/claude"
)

func TestMain(m *testing.M) {
	claudeKeychain = func() claude.Keychain { return nil }
	os.Exit(m.Run())
}
