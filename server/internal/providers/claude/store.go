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
	update(context.Context, credstore.Mutator) error
}

type fileStore struct{ path string }

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

func (s fileStore) update(ctx context.Context, mutator credstore.Mutator) error {
	return credstore.AtomicUpdate(ctx, s.path, mutator)
}

type keychainStore struct {
	keychain Keychain
	service  string
}

func (s keychainStore) load(ctx context.Context) (credstore.Snapshot, error) {
	data, err := s.keychain.Read(ctx, s.service)
	if err != nil {
		return credstore.Snapshot{}, err
	}
	return credstore.Snapshot{Data: data}, nil
}

func (s keychainStore) unchanged(ctx context.Context, snapshot credstore.Snapshot) (bool, error) {
	current, err := s.load(ctx)
	return bytes.Equal(current.Data, snapshot.Data), err
}
