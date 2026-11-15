//go:build !windows

package accounts

import (
	"context"
	"os/exec"
)

func claudeCommand(ctx context.Context, program string) *exec.Cmd {
	return exec.CommandContext(ctx, program, "auth", "login")
}
