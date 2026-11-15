//go:build windows

package claude

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// wslHomeDiscoveryEnabled lets tests keep native Windows discovery away from
// the developer's real WSL home. Tests disable it in TestMain; production
// always leaves it enabled.
var wslHomeDiscoveryEnabled = true

func defaultWSLHome() string {
	if !wslHomeDiscoveryEnabled {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "wsl.exe", "sh", "-lc", "printf '%s\n%s' \"$WSL_DISTRO_NAME\" \"$HOME\"")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(output), "\r", ""), "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) == "" || strings.TrimSpace(lines[1]) == "" {
		return ""
	}
	base := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(lines[1]), "/", `\`), `\`)
	if !strings.HasPrefix(base, `\`) {
		base = `\` + base
	}
	return filepath.Clean(`\\wsl.localhost\` + strings.TrimSpace(lines[0]) + base)
}
