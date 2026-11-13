package opencodego

import (
	"path/filepath"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/winprofile"
)

type credentialSourceKind int

const (
	credentialSourceNone credentialSourceKind = iota
	credentialSourceJSON
	credentialSourceSQLite
)

type credentialSource struct {
	path string
	name string
}

type credentialGroup struct {
	databasePath string
	authPath     string
	name         string
}

func localGroups(environment winprofile.Environment) []credentialGroup {
	directory := dataDirectory(environment)
	if directory == "" {
		return nil
	}
	group := credentialGroup{authPath: filepath.Join(directory, "auth.json"), name: "OpenCode Go CLI"}
	if database, ok := databasePath(environment, directory); ok {
		group.databasePath = database
	}
	return []credentialGroup{group}
}

func crossKernelGroups(environment winprofile.Environment) []credentialGroup {
	var homes []string
	var suffix string
	switch {
	case environment.GOOS == "windows":
		homes = environment.WSLHomes()
		suffix = " (WSL)"
	case environment.IsWSL():
		homes = environment.WindowsProfiles()
		suffix = " (Windows)"
	}
	groups := make([]credentialGroup, 0, len(homes))
	for _, home := range homes {
		directory := filepath.Join(home, ".local", "share", "opencode")
		groups = append(groups, credentialGroup{
			databasePath: filepath.Join(directory, "opencode.db"),
			authPath:     filepath.Join(directory, "auth.json"),
			name:         "OpenCode Go CLI" + suffix,
		})
	}
	return groups
}

func dataDirectory(environment winprofile.Environment) string {
	if value := strings.TrimSpace(environment.Env("XDG_DATA_HOME")); filepath.IsAbs(value) {
		return filepath.Join(value, "opencode")
	}
	if environment.Home == "" {
		return ""
	}
	return filepath.Join(environment.Home, ".local", "share", "opencode")
}

func databasePath(environment winprofile.Environment, directory string) (string, bool) {
	name := strings.TrimSpace(environment.Env("OPENCODE_DB"))
	switch {
	case name == ":memory:":
		return "", false
	case name == "":
		return filepath.Join(directory, "opencode.db"), true
	case filepath.IsAbs(name):
		return filepath.Clean(name), true
	default:
		return filepath.Join(directory, name), true
	}
}

func expandHome(path string, home string) string {
	switch {
	case path == "~":
		return home
	case strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`):
		return filepath.Join(home, path[2:])
	default:
		return path
	}
}
