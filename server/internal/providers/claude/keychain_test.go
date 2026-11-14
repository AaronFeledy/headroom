package claude

import (
	"context"
	"encoding/hex"
	"errors"
	"os/user"
	"reflect"
	"strings"
	"testing"
	"time"
)

func Test_keychainService_matches_Claude_Code_identity(t *testing.T) {
	// Given: independently computed Claude Code service names, including raw spellings.
	for _, tt := range []struct{ dir, want string }{
		{"", "Claude Code-credentials"},
		{"/Users/a/.claude-work", "Claude Code-credentials-c7ed87c8"},
		{"/Users/a/.claude-work/", "Claude Code-credentials-cb59a703"},
		{"/Users/a/caf\u00e9", "Claude Code-credentials-6843ed82"},
		{"/Users/a/cafe\u0301", "Claude Code-credentials-6843ed82"},
	} {
		t.Run(tt.dir, func(t *testing.T) {
			// When
			got := keychainService(tt.dir)
			// Then
			if got != tt.want {
				t.Fatalf("service = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_keychainAccount_uses_user_or_safe_fallback(t *testing.T) {
	// Given
	for _, tt := range []struct {
		env, user, want string
		err             error
	}{
		{"alice_2-x.y", "ignored", "alice_2-x.y", nil},
		{"", "local-user", "local-user", nil},
		{"invalid user", "ignored", "claude-code-user", nil},
		{"", "", "claude-code-user", errors.New("user unavailable")},
		{"", "domain\\user", "claude-code-user", nil},
	} {
		t.Run(tt.env+tt.user, func(t *testing.T) {
			// When
			got := keychainAccount(tt.env, func() (*user.User, error) { return &user.User{Username: tt.user}, tt.err })
			// Then
			if got != tt.want {
				t.Fatalf("account = %q, want %q", got, tt.want)
			}
		})
	}
}

func Test_systemKeychain_reads_with_exact_read_only_command(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "hex"}[encoded], func(t *testing.T) {
			// Given: the runner is a fixture; it never starts an OS command.
			data := claudeCredentialsJSON(credentialFixture{Access: "a", Refresh: "r", ExpiresAt: futureMillis(t)})
			reads := 0
			output := data
			if encoded {
				output = hex.EncodeToString([]byte(data))
			}
			keychain := systemKeychain{account: "alice", run: func(ctx context.Context, path string, args ...string) ([]byte, int, error) {
				reads++
				want := []string{"/usr/bin/security", "find-generic-password", "-a", "alice", "-w", "-s", "Claude Code-credentials"}
				if !reflect.DeepEqual(append([]string{path}, args...), want) {
					t.Errorf("unexpected security argv")
				}
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
					t.Error("read must have a five-second bound")
				}
				return []byte(output + "\n"), 0, nil
			}}
			// When
			got, err := keychain.Read(context.Background(), keychainService(""))
			// Then
			if err != nil || string(got) != data || reads != 1 {
				t.Fatalf("read failed: %v", err)
			}
		})
	}
}

func Test_systemKeychain_maps_failure_without_exposing_output(t *testing.T) {
	// Given
	for _, tt := range []struct {
		name      string
		code      int
		err, want error
		canceled  bool
	}{
		{"missing", 44, errors.New("secret stderr"), ErrCredentialsMissing, false},
		{"locked", 36, errors.New("secret stderr"), ErrKeychainLocked, false},
		{"other", 1, errors.New("secret stderr"), ErrKeychainUnavailable, false},
		{"launch", -1, errors.New("secret stderr"), ErrKeychainUnavailable, false},
		{"timeout", -1, context.DeadlineExceeded, ErrKeychainUnavailable, false},
		{"cancel", -1, context.Canceled, context.Canceled, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			keychain := systemKeychain{account: "alice", run: func(context.Context, string, ...string) ([]byte, int, error) {
				if tt.canceled {
					cancel()
				}
				return []byte("secret stdout"), tt.code, tt.err
			}}
			// When
			_, err := keychain.Read(ctx, keychainService(""))
			// Then
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("error leaked command output")
			}
		})
	}
}
