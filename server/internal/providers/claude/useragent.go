package claude

import (
	"path/filepath"
	"strconv"
	"strings"
)

// pinnedCLIVersion is the oldest identity known to receive banked resets.
const pinnedCLIVersion = "2.1.306"

// userAgent prefers the installed CLI version so Anthropic never withholds
// banked-reset data from an older identity. Detection never executes the CLI
// and never reads file contents.
func (c *Client) userAgent() string {
	version := installedCLIVersion(c.paths.homeDir)
	if version == "" {
		version = pinnedCLIVersion
	}
	return "claude-cli/" + version + " (external, cli)"
}

// installedCLIVersion resolves only the launcher link; installed CLI builds are
// version-named, so the resolved file name carries the identity.
func installedCLIVersion(home string) string {
	home = homeDir(home)
	if home == "" {
		return ""
	}
	target, err := filepath.EvalSymlinks(filepath.Join(home, ".local", "bin", "claude"))
	if err != nil {
		return ""
	}
	name := filepath.Base(target)
	if compareVersion(name, pinnedCLIVersion) < 0 {
		return ""
	}
	return name
}

// compareVersion orders plain MAJOR.MINOR.PATCH names and reports -1 when
// either side is not a numeric version.
func compareVersion(left, right string) int {
	a, ok := parseVersion(left)
	if !ok {
		return -1
	}
	b, ok := parseVersion(right)
	if !ok {
		return -1
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}

func parseVersion(value string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		if part == "" || len(part) > 9 {
			return out, false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return out, false
			}
		}
		number, err := strconv.Atoi(part)
		if err != nil {
			return out, false
		}
		out[i] = number
	}
	return out, true
}
