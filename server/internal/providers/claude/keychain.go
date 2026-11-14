package claude

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"regexp"
	"runtime"
	"time"

	"golang.org/x/text/unicode/norm"
)

// Keychain reads Claude Code's item without changing the operating system store.
type Keychain interface {
	Read(context.Context, string) ([]byte, error)
}

type commandRunner func(context.Context, string, ...string) ([]byte, int, error)

type systemKeychain struct {
	run     commandRunner
	account string
}

// SystemKeychain returns the read-only macOS adapter. Other platforms have none.
func SystemKeychain() Keychain {
	if runtime.GOOS != "darwin" {
		return nil
	}
	return systemKeychain{run: runSecurity, account: keychainAccount(os.Getenv("USER"), user.Current)}
}

func runSecurity(ctx context.Context, path string, args ...string) ([]byte, int, error) {
	output, err := exec.CommandContext(ctx, path, args...).Output()
	code := 0
	if err != nil {
		code = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
	}
	return output, code, err
}

var keychainAccountSafe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

func keychainAccount(name string, current func() (*user.User, error)) string {
	if name == "" {
		if info, err := current(); err == nil {
			name = info.Username
		}
	}
	if !keychainAccountSafe.MatchString(name) {
		return "claude-code-user"
	}
	return name
}

func keychainService(configDir string) string {
	const service = "Claude Code-credentials"
	if configDir == "" {
		return service
	}
	hash := sha256.Sum256([]byte(norm.NFC.String(configDir)))
	return service + "-" + hex.EncodeToString(hash[:4])
}

func (k systemKeychain) Read(ctx context.Context, service string) ([]byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	output, code, err := k.run(readCtx, "/usr/bin/security", "find-generic-password", "-a", k.account, "-w", "-s", service)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if errors.Is(readCtx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return nil, fmt.Errorf("keychain timed out: %w", ErrKeychainUnavailable)
	}
	if err != nil || code != 0 {
		switch code {
		case 44:
			return nil, ErrCredentialsMissing
		case 36:
			return nil, ErrKeychainLocked
		default:
			return nil, fmt.Errorf("keychain exit %d: %w", code, ErrKeychainUnavailable)
		}
	}
	output = bytes.TrimSpace(output)
	if !json.Valid(output) {
		if decoded, err := hex.DecodeString(string(output)); err == nil && json.Valid(decoded) {
			output = decoded
		}
	}
	return output, nil
}
