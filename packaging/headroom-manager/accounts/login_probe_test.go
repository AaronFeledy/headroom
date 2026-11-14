//go:build !darwin

package accounts

import (
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/accountconfig"
)

func TestNativeLoginWithRealProbe(t *testing.T) {
	testNativeLogin(t, accountconfig.ClaudeSignIn)
}
