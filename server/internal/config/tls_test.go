package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func Test_TLS_config_precedence_and_browser_credentials(t *testing.T) {
	// Given
	home := t.TempDir()
	path := filepath.Join(home, "config.yaml")
	if err := os.WriteFile(path, []byte("tls: off\ntls_cert_file: yaml-cert\ntls_key_file: yaml-key\nproviders:\n  cursor:\n    browser_credentials: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// When
	cfg, err := Load(context.Background(), LoadOptions{Args: []string{"--config", path, "--tls", "on", "--tls-cert-file", "flag-cert", "--tls-key-file", "flag-key"}, Env: []string{"HOME=" + home, "USAGE_TLS=auto", "USAGE_TLS_CERT_FILE=env-cert", "USAGE_TLS_KEY_FILE=env-key", "USAGE_PROVIDER_CURSOR_BROWSER_CREDENTIALS=true"}})
	// Then
	if err != nil || cfg.TLS != "on" || cfg.TLSCertFile != "flag-cert" || cfg.TLSKeyFile != "flag-key" || !cfg.Providers["cursor"].BrowserCredentials || cfg.TLSDir != filepath.Join(home, ".config", "headroom", "tls") {
		t.Fatalf("cfg=%#v err=%v", cfg, err)
	}
}

func Test_TLS_config_rejects_invalid_modes_pairs_and_browser_boolean(t *testing.T) {
	// Given
	for _, args := range [][]string{{"--tls", "bad"}, {"--tls-cert-file", "cert"}, {"--tls", "off", "--tls-cert-file", "cert", "--tls-key-file", "key"}} {
		t.Run(args[0]+args[1], func(t *testing.T) {
			home := t.TempDir()
			// When
			_, err := Load(context.Background(), LoadOptions{Args: args, Env: []string{"HOME=" + home}})
			// Then
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatal(err)
			}
		})
	}
	home := t.TempDir()
	_, err := Load(context.Background(), LoadOptions{Env: []string{"HOME=" + home, "USAGE_PROVIDER_CURSOR_BROWSER_CREDENTIALS=invalid"}})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
}
