package browsercookie

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func FirefoxProfiles(root string) []string {
	var preferred, profiles []string
	sections := readINI(filepath.Join(root, "profiles.ini"))
	for _, section := range sections {
		path := section["Path"]
		if path == "" {
			continue
		}
		if section["IsRelative"] != "0" {
			path = filepath.Join(root, filepath.FromSlash(path))
		}
		profiles = append(profiles, path)
		if section["Default"] == "1" {
			preferred = append(preferred, path)
		}
	}
	var installs []string
	for _, section := range readINI(filepath.Join(root, "installs.ini")) {
		if path := section["Default"]; path != "" {
			installs = append(installs, filepath.Join(root, filepath.FromSlash(path)))
		}
	}
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				profiles = append(profiles, filepath.Join(root, entry.Name()))
			}
		}
	}
	sort.Strings(installs)
	sort.Strings(preferred)
	sort.Strings(profiles)
	seen := map[string]bool{}
	var result []string
	for _, group := range [][]string{installs, preferred, profiles} {
		for _, path := range group {
			path = filepath.Clean(path)
			if !seen[path] {
				result = append(result, path)
				seen[path] = true
			}
		}
	}
	return result
}

func ChromiumProfiles(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var profiles []string
	for _, entry := range entries {
		if entry.IsDir() && (entry.Name() == "Default" || strings.HasPrefix(entry.Name(), "Profile ")) {
			profiles = append(profiles, filepath.Join(root, entry.Name()))
		}
	}
	return profiles
}

func readINI(path string) []map[string]string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var sections []map[string]string
	var current map[string]string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			current = map[string]string{}
			sections = append(sections, current)
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && current != nil {
			current[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}
	return sections
}
