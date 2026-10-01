package config

import (
	"fmt"
	"path/filepath"
)

func tlsDirectory(defaultPath string) string { return filepath.Join(filepath.Dir(defaultPath), "tls") }

func validateTLS(cfg Config) error {
	switch cfg.TLS {
	case "auto", "on", "off":
	default:
		return fmt.Errorf("tls must be auto, on, or off: %w", ErrInvalidConfig)
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		return fmt.Errorf("tls_cert_file and tls_key_file must be paired: %w", ErrInvalidConfig)
	}
	if cfg.TLS == "off" && cfg.TLSCertFile != "" {
		return fmt.Errorf("tls off cannot use certificate files: %w", ErrInvalidConfig)
	}
	return nil
}
