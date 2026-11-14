package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func Test_systemKeychain_reads_disposable_native_keychain(t *testing.T) {
	if os.Getenv("HEADROOM_KEYCHAIN_FIXTURE") != "1" {
		t.Skip("set HEADROOM_KEYCHAIN_FIXTURE=1 to use a disposable macOS Keychain")
	}
	// Given: only synthetic items in a private keychain, never the login keychain.
	searchList := keychainFixturePaths(t, keychainFixtureCommand(t, "list-keychains", "-d", "user"))
	defaultList := keychainFixturePaths(t, keychainFixtureCommand(t, "default-keychain", "-d", "user"))
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal("generate fixture password")
	}
	password := hex.EncodeToString(random[:])
	file := filepath.Join(t.TempDir(), "fixture.keychain-db")
	created := false
	t.Cleanup(func() {
		commands := [][]string{append([]string{"list-keychains", "-d", "user", "-s"}, searchList...)}
		if len(defaultList) == 1 {
			commands = append(commands, []string{"default-keychain", "-d", "user", "-s", defaultList[0]})
		}
		if created {
			commands = append(commands, []string{"delete-keychain", file})
		}
		for _, args := range commands {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_, code, err := runSecurity(ctx, "/usr/bin/security", args...)
			cancel()
			if err != nil || code != 0 {
				t.Errorf("fixture cleanup %s failed (exit %d)", args[0], code)
			}
		}
	})
	keychainFixtureCommand(t, "create-keychain", "-p", password, file)
	created = true
	keychainFixtureCommand(t, "unlock-keychain", "-p", password, file)
	keychainFixtureCommand(t, "set-keychain-settings", file)
	account := "headroom-fixture"
	service := "headroom-fixture-" + password[:16]
	data := `{"fixture":"synthetic-sign-in"}`
	keychainFixtureCommand(t, "add-generic-password", "-a", account, "-s", service, "-w", data, file)
	adapter := systemKeychain{run: runSecurity, account: account, file: file}
	t.Run("present item returns JSON bytes", func(t *testing.T) {
		// When
		got, err := adapter.Read(context.Background(), service)
		// Then
		if err != nil || string(got) != data {
			t.Fatalf("disposable item read failed: %v", err)
		}
	})
	t.Run("missing service is signed out", func(t *testing.T) {
		// When
		_, err := adapter.Read(context.Background(), service+"-missing")
		// Then
		if !errors.Is(err, ErrCredentialsMissing) {
			t.Fatalf("missing fixture service: %v", err)
		}
	})
	t.Run("locked keychain fails within timeout", func(t *testing.T) {
		// Given
		keychainFixtureCommand(t, "lock-keychain", file)
		// When
		start := time.Now()
		_, err := adapter.Read(context.Background(), service)
		// Then
		if !errors.Is(err, ErrKeychainLocked) && !errors.Is(err, ErrKeychainUnavailable) {
			t.Fatalf("locked fixture keychain: %v", err)
		}
		if time.Since(start) > 7*time.Second {
			t.Fatal("locked keychain read exceeded its five-second timeout and scheduling margin")
		}
	})
}

func keychainFixtureCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, code, err := runSecurity(ctx, "/usr/bin/security", args...)
	if err != nil || code != 0 {
		t.Fatalf("fixture %s failed (exit %d)", args[0], code)
	}
	return output
}

func keychainFixturePaths(t *testing.T, output []byte) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path, err := strconv.Unquote(strings.TrimSpace(line))
		if err != nil {
			t.Fatal("could not parse user keychain list")
		}
		paths = append(paths, path)
	}
	return paths
}
