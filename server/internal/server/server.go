package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type RunOptions struct {
	Listener             net.Listener
	Handler              http.Handler
	Logger               *slog.Logger
	Ready                chan<- net.Addr
	TLSCertificateSHA256 string
}

func Run(ctx context.Context, opts RunOptions) error {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	handler := opts.Handler
	if handler == nil {
		handler = http.NotFoundHandler()
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	done := make(chan error, 1)
	go func() {
		if opts.Ready != nil {
			opts.Ready <- opts.Listener.Addr()
		}
		attrs := []slog.Attr{slog.String("addr", opts.Listener.Addr().String())}
		if opts.TLSCertificateSHA256 != "" {
			attrs = append(attrs, slog.Bool("tls", true), slog.String("tls_certificate_sha256", opts.TLSCertificateSHA256))
		}
		logger.LogAttrs(ctx, slog.LevelInfo, "server listening", attrs...)
		done <- srv.Serve(opts.Listener)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		err := <-done
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	case err := <-done:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve: %w", err)
		}
		return nil
	}
}
