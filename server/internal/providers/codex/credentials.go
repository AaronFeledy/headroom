package codex

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type CredentialStore struct {
	mu        sync.RWMutex
	path      string
	source    CredentialSource
	discovery DiscoveryOptions
	creds     Credentials
	snapshot  credstore.Snapshot
	loaded    bool
	warn      func(error)
}

func NewCredentialStore(opts CredentialOptions) *CredentialStore {
	source := opts.Source
	if source != CredentialSourceOpenCode {
		source = CredentialSourceCodex
	}
	return &CredentialStore{path: opts.Path, source: source, discovery: opts.Discovery}
}

func (s *CredentialStore) Load(ctx context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(ctx)
}

func (s *CredentialStore) Current(ctx context.Context) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		return s.loadLocked(ctx)
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return Credentials{}, fmt.Errorf("stat codex credentials %s: %w", s.path, ErrCredentialsMissing)
	}
	if !info.ModTime().Equal(s.snapshot.ModTime) {
		return s.loadLocked(ctx)
	}
	return s.creds, nil
}

// NeedsRefresh reports an OpenCode-style expiry that has already passed. Codex
// source tokens carry no expiry, so they refresh only after a 401 or 403.
func (s *CredentialStore) NeedsRefresh(now time.Time) bool {
	s.mu.RLock()
	creds := s.creds
	loaded := s.loaded
	s.mu.RUnlock()
	if !loaded || creds.RefreshToken == "" || creds.ExpiresAt == nil {
		return false
	}
	return !now.Before(*creds.ExpiresAt)
}

// Refresh spends the caller's token pair through credstore.RefreshTokens. A
// store that already holds a different pair is adopted without an exchange, so
// a rejected pair only reaches the token endpoint while it is still current.
func (s *CredentialStore) Refresh(ctx context.Context, stale Credentials, exchange func(Credentials) (RefreshedTokens, error)) error {
	path, source, err := s.target(ctx)
	if err != nil {
		return err
	}
	codec := tokenCodec(path, source, s.warn)
	_, _, err = credstore.RefreshTokens(ctx, path, tokenPair(stale), codec, func(file []byte) (RefreshedTokens, error) {
		current, parseErr := parseCredentials(file, path, source)
		if parseErr != nil {
			return RefreshedTokens{}, parseErr
		}
		return exchange(current)
	})
	if err != nil {
		return fmt.Errorf("save codex credentials: %w", err)
	}
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
	defer cancel()
	_, err = s.Load(loadCtx)
	return err
}

func (s *CredentialStore) target(ctx context.Context) (string, CredentialSource, error) {
	s.mu.RLock()
	path, source, loaded := s.path, s.source, s.loaded
	s.mu.RUnlock()
	if loaded && path != "" {
		return path, source, nil
	}
	if _, err := s.Load(ctx); err != nil {
		return "", CredentialSourceCodex, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.path, s.source, nil
}

func tokenPair(creds Credentials) credstore.TokenPair {
	return credstore.TokenPair{Access: creds.AccessToken, Refresh: creds.RefreshToken}
}

func tokenCodec(path string, source CredentialSource, warn func(error)) credstore.TokenCodec[RefreshedTokens] {
	return credstore.TokenCodec[RefreshedTokens]{
		Pair: func(file []byte) (credstore.TokenPair, error) {
			creds, err := parseCredentials(file, path, source)
			if err != nil {
				return credstore.TokenPair{}, err
			}
			return tokenPair(creds), nil
		},
		Apply: func(file []byte, tokens RefreshedTokens) ([]byte, error) {
			return mutateCredentials(file, source, tokens)
		},
		Of: func(tokens RefreshedTokens) credstore.TokenPair {
			return credstore.TokenPair{Access: tokens.AccessToken, Refresh: tokens.RefreshToken}
		},
		Warn: warn,
	}
}

func (s *CredentialStore) loadLocked(ctx context.Context) (Credentials, error) {
	path := s.path
	source := s.source
	if path == "" {
		discovered, err := DiscoverAuthPath(ctx, s.discovery)
		if err != nil {
			return Credentials{}, err
		}
		path = discovered.Path
		source = discovered.Source
	}
	snapshot, err := credstore.LoadSnapshot(ctx, path)
	if err != nil {
		return Credentials{}, fmt.Errorf("load codex credentials %s: %w", path, ErrCredentialsMissing)
	}
	creds, err := parseCredentials(snapshot.Data, path, source)
	if err != nil {
		return Credentials{}, err
	}
	s.path = path
	s.source = source
	s.creds = creds
	s.snapshot = snapshot
	s.loaded = true
	return creds, nil
}
