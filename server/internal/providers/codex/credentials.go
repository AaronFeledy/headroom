package codex

import (
	"context"
	"fmt"
	"os"
	"strings"
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

func (s *CredentialStore) NeedsRefresh(now time.Time) bool {
	s.mu.RLock()
	creds := s.creds
	loaded := s.loaded
	s.mu.RUnlock()
	if !loaded || strings.TrimSpace(creds.RefreshToken) == "" {
		return false
	}
	if creds.ExpiresAt != nil {
		return !now.Before(creds.ExpiresAt.Add(-5 * time.Minute))
	}
	return creds.LastRefresh == nil || now.Sub(*creds.LastRefresh) > refreshWindow
}

func (s *CredentialStore) ReloadIfChanged(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		_, err := s.loadLocked(ctx)
		return true, err
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return false, fmt.Errorf("stat codex credentials %s: %w", s.path, ErrCredentialsMissing)
	}
	if info.ModTime().Equal(s.snapshot.ModTime) {
		return false, nil
	}
	_, err = s.loadLocked(ctx)
	return true, err
}

// Refresh locks the resolved credentials file, compares the stored token pair to
// stale, and calls exchange only when the pair still matches. exchange must not
// write the store. After a successful commit, credentials are reloaded with a
// detached bounded context.
func (s *CredentialStore) Refresh(ctx context.Context, stale Credentials, exchange func(Credentials) (RefreshedTokens, error)) error {
	s.mu.Lock()
	path := s.path
	source := s.source
	s.mu.Unlock()
	if path == "" {
		if _, err := s.Load(ctx); err != nil {
			return err
		}
		s.mu.RLock()
		path = s.path
		source = s.source
		s.mu.RUnlock()
	}
	err := credstore.Refresh(ctx, path, func(current []byte) (credstore.Mutator, error) {
		parsed, err := parseCredentials(current, path, source)
		if err != nil {
			return nil, err
		}
		if parsed.AccessToken != stale.AccessToken || parsed.RefreshToken != stale.RefreshToken {
			return nil, nil
		}
		tokens, err := exchange(parsed)
		if err != nil {
			return nil, err
		}
		return func(latest []byte) ([]byte, error) {
			latestParsed, err := parseCredentials(latest, path, source)
			if err != nil {
				return nil, err
			}
			if latestParsed.AccessToken != stale.AccessToken || latestParsed.RefreshToken != stale.RefreshToken {
				return latest, nil
			}
			return mutateCredentials(latest, source, tokens)
		}, nil
	})
	if err != nil {
		return fmt.Errorf("save codex credentials: %w", err)
	}
	loadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), tokenRequestTimeout)
	defer cancel()
	_, err = s.Load(loadCtx)
	return err
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
