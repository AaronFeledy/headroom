package claude

import (
	"regexp"
	"strings"
)

var posixEnvDirSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func accountSignInCommand(goos, dir string) string {
	if goos == "windows" {
		escaped := strings.ReplaceAll(dir, "'", "''")
		return "$previous = $env:CLAUDE_CONFIG_DIR; $env:CLAUDE_CONFIG_DIR = '" + escaped + "'; try { claude auth login } finally { $env:CLAUDE_CONFIG_DIR = $previous }"
	}
	assignment := dir
	if !posixEnvDirSafe.MatchString(dir) {
		assignment = "'" + strings.ReplaceAll(dir, "'", `'\''`) + "'"
	}
	return "CLAUDE_CONFIG_DIR=" + assignment + " claude auth login"
}
