package tlsidentity

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

type CertificateOptions struct {
	Random     io.Reader
	Now        time.Time
	BoundIP    net.IP
	Hostname   string
	CommonName string
	Days       int
}

func Generate(options CertificateOptions) ([]byte, tls.Certificate, error) {
	key, err := rsa.GenerateKey(options.Random, 2048)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("generate TLS key: %w", err)
	}
	serial, err := rand.Int(options.Random, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("generate TLS serial: %w", err)
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}
	keyID := sha256.Sum256(x509.MarshalPKCS1PublicKey(&key.PublicKey))
	ips := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}
	if options.BoundIP != nil && !options.BoundIP.IsUnspecified() && !options.BoundIP.Equal(ips[0]) && !options.BoundIP.Equal(ips[1]) {
		ips = append(ips, options.BoundIP)
	}
	names := []string{"localhost"}
	if options.Hostname != "" && options.Hostname != "localhost" {
		names = append(names, options.Hostname)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: options.CommonName}, NotBefore: options.Now.Add(-5 * time.Minute), NotAfter: options.Now.AddDate(0, 0, options.Days), DNSNames: names, IPAddresses: ips, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, SubjectKeyId: keyID[:]}
	der, err := x509.CreateCertificate(options.Random, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, tls.Certificate{}, fmt.Errorf("generate TLS certificate: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: template}, nil
}
