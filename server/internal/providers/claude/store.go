package claude

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

type credentialStore interface {
	load(context.Context) (credstore.Snapshot, error)
	unchanged(context.Context, credstore.Snapshot) (bool, error)
}

type writableCredentialStore interface {
	credentialStore
	refresh(context.Context, credstore.TokenPair, func(file []byte) (credentialUpdate, error)) (credstore.Outcome, error)
}

type fileStore struct {
	path          string
	source        credentialSource
	claudeCodeDir string
	lockConfig    claudeCodeLockConfig
	warn          func(error)
}

func (s fileStore) load(ctx context.Context) (credstore.Snapshot, error) {
	snapshot, err := credstore.LoadSnapshot(ctx, s.path)
	if errors.Is(err, os.ErrNotExist) {
		return credstore.Snapshot{}, credentialsError{path: s.path, err: ErrCredentialsMissing}
	}
	if err != nil {
		return credstore.Snapshot{}, credentialsError{path: s.path, err: err}
	}
	return snapshot, nil
}

func (s fileStore) unchanged(ctx context.Context, snapshot credstore.Snapshot) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	info, err := os.Stat(s.path)
	if err != nil {
		return false, err
	}
	return info.ModTime().Equal(snapshot.ModTime), nil
}

// refresh takes Claude Code's mkdir lock when claudeCodeDir is set, then routes
// the refresh through credstore.RefreshTokens. The lock stays held around the
// exchange, and the exchange callback verifies and renews it immediately before
// the token POST. A rotation that was already applied to the file is adopted
// without an exchange.
func (s fileStore) refresh(ctx context.Context, spent credstore.TokenPair,
	exchange func(file []byte) (credentialUpdate, error)) (outcome credstore.Outcome, err error) {
	var lock *claudeCodeLock
	if s.claudeCodeDir != "" {
		lock, err = acquireClaudeCodeLock(ctx, s.claudeCodeDir, s.lockConfig)
		if err != nil {
			return credstore.Adopted, err
		}
		defer func() { err = errors.Join(err, lock.release()) }()
	}
	codec := credstore.TokenCodec[credentialUpdate]{
		Pair: func(file []byte) (credstore.TokenPair, error) {
			parsed, parseErr := parseCredentials(s.path, s.source, credstore.Snapshot{Data: file})
			if parseErr != nil {
				return credstore.TokenPair{}, parseErr
			}
			return credstore.TokenPair{Access: parsed.accessToken, Refresh: parsed.refreshToken}, nil
		},
		// Write the section Pair read: a Claude Code path can hold an
		// OpenCode-shaped sign-in, which parseCredentials reads from "anthropic".
		Apply: func(file []byte, update credentialUpdate) ([]byte, error) {
			parsed, parseErr := parseCredentials(s.path, s.source, credstore.Snapshot{Data: file})
			if parseErr != nil {
				return nil, parseErr
			}
			return applyCredentialUpdate(file, parsed.source, update)
		},
		Of: func(update credentialUpdate) credstore.TokenPair {
			return credstore.TokenPair{Access: update.refreshed.AccessToken, Refresh: update.refreshed.RefreshToken}
		},
		Warn: s.warn,
	}
	_, outcome, err = credstore.RefreshTokens(ctx, s.path, spent, codec, func(file []byte) (credentialUpdate, error) {
		if lock != nil {
			if lock.isLost() {
				return credentialUpdate{}, errClaudeCodeLockLost
			}
			if err := lock.heartbeatStep(); err != nil {
				return credentialUpdate{}, err
			}
		}
		return exchange(file)
	})
	return outcome, err
}

type keychainStore struct {
	keychain  Keychain
	service   string
	alternate string
}

func (s keychainStore) load(ctx context.Context) (credstore.Snapshot, error) {
	data, err := s.keychain.Read(ctx, s.service)
	if errors.Is(err, ErrCredentialsMissing) && s.alternate != "" {
		data, err = s.keychain.Read(ctx, s.alternate)
	}
	if err != nil {
		return credstore.Snapshot{}, err
	}
	return credstore.Snapshot{Data: data}, nil
}

func (s keychainStore) unchanged(ctx context.Context, snapshot credstore.Snapshot) (bool, error) {
	current, err := s.load(ctx)
	return bytes.Equal(current.Data, snapshot.Data), err
}
