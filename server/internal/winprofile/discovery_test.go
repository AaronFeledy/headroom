package winprofile

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_WindowsProfiles_automount_exclusions_and_USER_preference(t *testing.T) {
	// Given
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "wsl.conf"), []byte("[automount]\nroot=/drives/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Zoe", "alice", "Public", "Default", "Default User", "All Users"} {
		if err := os.MkdirAll(filepath.Join(root, "drives", "c", "Users", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, preferred := range []string{"", "ALICE"} {
		t.Run(preferred, func(t *testing.T) {
			e := Environment{GOOS: "linux", Root: root, Env: func(key string) string {
				switch key {
				case "WSL_DISTRO_NAME":
					return "fixture"
				case "USER":
					return preferred
				}
				return ""
			}}
			// When
			profiles := e.WindowsProfiles()
			// Then
			if preferred == "" {
				if len(profiles) != 2 || filepath.Base(profiles[0]) != "Zoe" || filepath.Base(profiles[1]) != "alice" {
					t.Fatal(profiles)
				}
			} else {
				if len(profiles) != 1 || filepath.Base(profiles[0]) != "alice" {
					t.Fatal(profiles)
				}
			}
		})
	}
}

func Test_WSLHomes_discovers_without_executables(t *testing.T) {
	// Given
	root := t.TempDir()
	for _, name := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(root, "Fixture", "home", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e := Environment{GOOS: "windows", WSLRoot: root, Env: func(key string) string {
		if key == "USERNAME" {
			return "bob"
		}
		return ""
	}}
	// When
	homes := e.WSLHomes()
	// Then
	if len(homes) != 2 || filepath.Base(homes[0]) != "bob" {
		t.Fatal(homes)
	}
}
