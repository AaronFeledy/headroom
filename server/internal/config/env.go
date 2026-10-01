package config

import "strings"

func parseEnv(env []string) map[string]string {
	vars := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, found := strings.Cut(entry, "=")
		if found {
			vars[key] = value
		}
	}
	return vars
}

func providerNameFromEnv(key string, prefix string, suffix string) string {
	name := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
	return strings.ToLower(name)
}
