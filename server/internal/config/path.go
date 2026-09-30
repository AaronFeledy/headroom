package config

import (
	"fmt"
	"path/filepath"
)

func DefaultPath(goos string, env []string) (string, error) {
	base, err := configBase(goos, parseEnv(env))
	if err != nil {
		return "", err
	}
	name := "headroom"
	if goos == "windows" {
		name = "Headroom"
	}
	return filepath.Join(base, name, "config.yaml"), nil
}

func configBase(goos string, vars map[string]string) (string, error) {
	if goos == "windows" {
		appData := vars["APPDATA"]
		if appData == "" {
			return "", fmt.Errorf("APPDATA missing: %w", ErrInvalidConfig)
		}
		return appData, nil
	}

	if xdg := vars["XDG_CONFIG_HOME"]; xdg != "" {
		return xdg, nil
	}
	home := vars["HOME"]
	if home == "" {
		return "", fmt.Errorf("HOME missing: %w", ErrInvalidConfig)
	}
	return filepath.Join(home, ".config"), nil
}
