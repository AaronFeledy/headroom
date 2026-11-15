package accounts

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

func claudeCommand(ctx context.Context, program string) *exec.Cmd {
	switch strings.ToLower(filepath.Ext(program)) {
	case ".cmd", ".bat":
		// Claude Code's npm installation uses a command script on Windows.
		cmd := exec.CommandContext(ctx, "cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c ""` + program + `" auth login"`}
		return cmd
	default:
		return exec.CommandContext(ctx, program, "auth", "login")
	}
}
