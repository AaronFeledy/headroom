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
	refresh(context.Context, func([]byte) (credstore.Mutator, error)) error
}

type fileStore struct {
	path          string
	claudeCodeDir string
	lockConfig    claudeCodeLockConfig
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

// refresh takes Claude Code's mkdir lock when claudeCodeDir is set, then
// credstore.Refresh. prepare is not called when the lock was lost before the
// exchange. A synchronous heartbeat runs immediately before prepare (and thus
// before any token POST). After a POST, the returned edit still commits while
// the spent token pair remains in the file, even if the lock is lost.
func (s fileStore) refresh(ctx context.Context, prepare func([]byte) (credstore.Mutator, error)) (err error) {
	var lock *claudeCodeLock
	if s.claudeCodeDir != "" {
		lock, err = acquireClaudeCodeLock(ctx, s.claudeCodeDir, s.lockConfig)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, lock.release()) }()
	}
	return credstore.Refresh(ctx, s.path, func(current []byte) (credstore.Mutator, error) {
		if lock != nil {
			if lock.isLost() {
				return nil, errClaudeCodeLockLost
			}
			if err := lock.heartbeatStep(); err != nil {
				return nil, err
			}
		}
		return prepare(current)
	})
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
