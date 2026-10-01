// Package winprofile discovers cross-kernel credential locations without executing Windows programs.
package winprofile

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Environment struct {
	GOOS    string
	Home    string
	Env     func(string) string
	Root    string
	WSLHome string
	WSLRoot string
}

func (e Environment) WSLHomes() []string {
	if e.GOOS != "windows" {
		return nil
	}
	if e.WSLHome != "" {
		return []string{e.WSLHome}
	}
	root := e.WSLRoot
	if root == "" {
		root = `\\wsl.localhost`
	}
	distros, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var result []string
	for _, distro := range distros {
		usersRoot := filepath.Join(root, distro.Name(), "home")
		users, err := os.ReadDir(usersRoot)
		if err != nil {
			continue
		}
		var preferred, others []string
		for _, user := range users {
			if !user.IsDir() {
				continue
			}
			path := filepath.Join(usersRoot, user.Name())
			if strings.EqualFold(user.Name(), e.Env("USERNAME")) {
				preferred = append(preferred, path)
			} else {
				others = append(others, path)
			}
		}
		result = append(result, preferred...)
		result = append(result, others...)
	}
	return result
}

func (e Environment) Path(path string) string {
	if e.Root == "" {
		return filepath.FromSlash(path)
	}
	return filepath.Join(e.Root, filepath.FromSlash(strings.TrimLeft(path, "/")))
}

func (e Environment) IsWSL() bool {
	if e.GOOS != "linux" {
		return false
	}
	if e.Env("WSL_DISTRO_NAME") != "" {
		return true
	}
	if _, err := os.Stat(e.Path("/proc/sys/fs/binfmt_misc/WSLInterop")); err == nil {
		return true
	}
	data, err := os.ReadFile(e.Path("/proc/version"))
	return err == nil && strings.Contains(strings.ToLower(string(data)), "microsoft")
}

func (e Environment) WindowsProfiles() []string {
	if !e.IsWSL() {
		return nil
	}
	root := "/mnt/"
	if data, err := os.ReadFile(e.Path("/etc/wsl.conf")); err == nil {
		section := ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = strings.ToLower(line)
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok && section == "[automount]" && strings.TrimSpace(key) == "root" {
				root = strings.Trim(strings.TrimSpace(value), "\"'")
			}
		}
	}
	users := e.Path(filepath.ToSlash(filepath.Join(root, "c", "Users")))
	entries, err := os.ReadDir(users)
	if err != nil {
		return nil
	}
	var profiles []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		switch strings.ToLower(entry.Name()) {
		case "public", "default", "default user", "all users":
			continue
		}
		path := filepath.Join(users, entry.Name())
		if _, err := os.ReadDir(path); err != nil {
			continue
		}
		if strings.EqualFold(entry.Name(), e.Env("USER")) {
			return []string{path}
		}
		profiles = append(profiles, path)
	}
	sort.Strings(profiles)
	return profiles
}
