package app

import (
	"log/slog"
	"net"
	"os"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/config"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/tlsidentity"
)

func prepareTLS(listener net.Listener, cfg config.Config, logger *slog.Logger, options desktopSessionOptions) (net.Listener, string, error) {
	loopback, err := api.IsLoopbackListenAddr(cfg.ListenAddr)
	if err != nil {
		return listener, "", err
	}
	if cfg.TLS == "off" || (cfg.TLS == "auto" && loopback && cfg.TLSCertFile == "") {
		return listener, "", nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	hostname, err := os.Hostname()
	if err != nil {
		return listener, "", err
	}
	var ip net.IP
	if address, ok := listener.Addr().(*net.TCPAddr); ok {
		ip = address.IP
	}
	certificate, rotated, err := tlsidentity.Load(tlsidentity.Options{Directory: cfg.TLSDir, CertFile: cfg.TLSCertFile, KeyFile: cfg.TLSKeyFile, Now: options.now(), Hostname: hostname, BoundIP: ip})
	if err != nil {
		if cfg.TLS == "auto" && cfg.TLSCertFile == "" {
			logger.Warn("TLS identity unavailable; serving HTTP only", slog.Any("error", err))
			return listener, "", nil
		}
		return listener, "", err
	}
	if rotated {
		logger.Info("TLS identity generated or rotated")
	}
	return tlsidentity.Listen(listener, certificate), tlsidentity.Fingerprint(certificate.Certificate[0]), nil
}
