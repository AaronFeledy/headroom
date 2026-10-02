package tlsidentity

import (
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

type Options struct {
	Directory string
	CertFile  string
	KeyFile   string
	Now       time.Time
	Hostname  string
	BoundIP   net.IP
}

func Load(options Options) (certificate tls.Certificate, rotated bool, err error) {
	if options.CertFile != "" {
		certificate, err = tls.LoadX509KeyPair(options.CertFile, options.KeyFile)
		return certificate, false, err
	}
	if options.Directory == "" {
		return certificate, false, errors.New("TLS identity directory unavailable")
	}
	certPath, keyPath := filepath.Join(options.Directory, "cert.pem"), filepath.Join(options.Directory, "key.pem")
	certificate, err = tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		leaf, parseErr := x509.ParseCertificate(certificate.Certificate[0])
		if parseErr == nil && leaf.NotAfter.Sub(options.Now) >= 30*24*time.Hour && !options.Now.Before(leaf.NotBefore) {
			certificate.Leaf = leaf
			if err := os.Chmod(options.Directory, 0o700); err != nil {
				return certificate, false, err
			}
			if err := os.Chmod(keyPath, 0o600); err != nil {
				return certificate, false, err
			}
			return certificate, false, nil
		}
	}
	certPEM, certificate, err := Generate(CertificateOptions{Random: rand.Reader, Now: options.Now, BoundIP: options.BoundIP, Hostname: options.Hostname, CommonName: "Headroom", Days: 397})
	if err != nil {
		return certificate, false, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if err != nil {
		return certificate, false, fmt.Errorf("encode TLS key: %w", err)
	}
	if err := os.MkdirAll(options.Directory, 0o700); err != nil {
		return certificate, false, fmt.Errorf("create TLS directory: %w", err)
	}
	if err := os.Chmod(options.Directory, 0o700); err != nil {
		return certificate, false, err
	}
	if err := atomicWrite(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return certificate, false, err
	}
	if err := atomicWrite(certPath, certPEM); err != nil {
		return certificate, false, err
	}
	return certificate, true, nil
}

func atomicWrite(path string, data []byte) (err error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".tls-*")
	if err != nil {
		return fmt.Errorf("create TLS file: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(file.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if _, err = file.Write(data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
