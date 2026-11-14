package claude

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
)

type storeCandidate struct {
	store  credentialStore
	key    string
	source credentialSource
}

func (c *Client) credentialCandidates() []storeCandidate {
	opts := c.paths
	file := func(path string, source credentialSource) storeCandidate {
		return storeCandidate{store: fileStore{path: path}, key: path, source: source}
	}
	if opts.configuredPath != "" {
		return []storeCandidate{file(expandHome(opts.configuredPath, opts.homeDir), credentialSourceClaude)}
	}
	var candidates []storeCandidate
	if c.goos == "darwin" && c.keychain != nil {
		service := keychainService(opts.configDir)
		candidates = append(candidates, storeCandidate{store: keychainStore{keychain: c.keychain, service: service}, key: service, source: credentialSourceClaude})
	}
	dir := opts.configDir
	if dir == "" {
		dir = filepath.Join(homeDir(opts.homeDir), ".claude")
	}
	candidates = append(candidates, file(filepath.Join(dir, ".credentials.json"), credentialSourceClaude))
	if opts.configDir != "" {
		return candidates
	}
	wslHome := ""
	if c.goos == "windows" {
		wslHome = opts.wslHomeDir
		if wslHome == "" {
			wslHome = defaultWSLHome()
		}
		if wslHome != "" {
			candidates = append(candidates, file(filepath.Join(wslHome, ".claude", ".credentials.json"), credentialSourceClaude))
		}
	}
	candidates = append(candidates, file(openCodePath(opts, wslHome), credentialSourceOpenCode))
	return candidates
}

func (c *Client) loadCredentials(ctx context.Context) (resolved *credentials, err error) {
	defer func() {
		c.stateMu.Lock()
		c.loaded = resolved
		c.stateMu.Unlock()
	}()
	var keychainErr error
	for _, candidate := range c.credentialCandidates() {
		snapshot, loadErr := candidate.store.load(ctx)
		switch {
		case errors.Is(loadErr, ErrCredentialsMissing):
			continue
		case keychainAccessError(loadErr):
			keychainErr = loadErr
			continue
		case loadErr != nil:
			return nil, loadErr
		}
		parsed, parseErr := parseCredentials(candidate.key, candidate.source, snapshot)
		if parseErr != nil {
			return nil, parseErr
		}
		parsed.store = candidate.store
		parsed.rediscover = keychainErr != nil
		return parsed, nil
	}
	if keychainErr != nil {
		return nil, keychainErr
	}
	return nil, ErrCredentialsMissing
}

func (c *Client) currentCredentials(ctx context.Context) (*credentials, error) {
	c.stateMu.Lock()
	loaded := c.loaded
	c.stateMu.Unlock()
	if loaded != nil {
		return loaded, nil
	}
	return c.loadCredentials(ctx)
}

func (c *Client) ensureFreshCredentials(ctx context.Context, creds *credentials) (*credentials, error) {
	unchanged, err := creds.store.unchanged(ctx, creds.snapshot)
	if err != nil || !unchanged || creds.rediscover {
		creds, err = c.loadCredentials(ctx)
		if err != nil {
			return nil, err
		}
	}
	if creds.expiresAt.After(time.Now().UTC().Add(expirySkew)) {
		return creds, nil
	}
	if err := c.refreshCredentials(ctx, creds, refreshModeProactive); err != nil {
		return nil, err
	}
	return c.currentCredentials(ctx)
}

func expandConfigDir(path, home string) string {
	if path == "~" {
		return homeDir(home)
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		return homeDir(home) + path[1:]
	}
	return path
}
