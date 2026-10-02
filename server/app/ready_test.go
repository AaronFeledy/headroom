package app

import (
	"context"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// Only provider-disabled startup is exercised. This never invokes reset actions
// or code that can consume a banked reset.
func TestRunReadyRejectsInvalidStartup(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	for _, tc := range []struct {
		name      string
		args, env []string
	}{
		{"occupied listener", []string{"--listen-addr", occupied.Addr().String()}, disabledProviderEnv()},
		{"unsafe bind", []string{"--listen-addr", "0.0.0.0:0", "--auth-token", ""}, disabledProviderEnv()},
		{"invalid provider", []string{"--listen-addr", "127.0.0.1:0"}, append(disabledProviderEnv(), "USAGE_PROVIDER_UNKNOWN_ENABLED=true")},
		{"zero poll interval", []string{"--listen-addr", "127.0.0.1:0", "--poll-interval", "0s"}, disabledProviderEnv()},
		{"negative poll interval", []string{"--listen-addr", "127.0.0.1:0", "--poll-interval", "-1s"}, disabledProviderEnv()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			args := append([]string{"--config", filepath.Join(t.TempDir(), "missing.yaml")}, tc.args...)
			err := RunWithReady(context.Background(), args, tc.env, discardLogger(), "test", nil, io.Discard, func() error { called = true; return nil })
			if err == nil || called {
				t.Fatalf("startup error=%v; ready called=%v", err, called)
			}
		})
	}
}

func TestRunReadyFailureClosesBoundListener(t *testing.T) {
	expected := errors.New("private readiness acknowledgement failed")
	var bound net.Listener
	calls := 0
	args := []string{"--config", filepath.Join(t.TempDir(), "missing.yaml"), "--listen-addr", "127.0.0.1:0"}
	err := runContext(context.Background(), args, disabledProviderEnv(), discardLogger(), desktopSessionOptions{
		listen: func(network, address string) (net.Listener, error) {
			var err error
			bound, err = net.Listen(network, address)
			return bound, err
		},
		ready: func() error {
			calls++
			if bound == nil {
				t.Fatal("ready preceded listener binding")
			}
			return expected
		},
	})
	if !errors.Is(err, expected) || calls != 1 {
		t.Fatalf("error=%v; calls=%d", err, calls)
	}
	_ = bound.(*net.TCPListener).SetDeadline(time.Now().Add(time.Second))
	if _, err := bound.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("listener still open: %v", err)
	}
}
