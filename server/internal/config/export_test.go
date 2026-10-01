package config

import (
	"context"
	"os"
)

// LoadForOS exercises the same loader with platform semantics and local hooks.
func LoadForOS(ctx context.Context, opts LoadOptions, goos string) (Config, error) {
	return loadForOS(ctx, opts, goos, migrationOps{rename: os.Rename, symlink: os.Symlink, link: os.Link})
}

type MigrationOperations = migrationOps

func MigrationHooks(rename, symlink, link func(string, string) error) MigrationOperations {
	return migrationOps{rename: rename, symlink: symlink, link: link}
}

func LoadWithMigration(ctx context.Context, opts LoadOptions, goos string, ops MigrationOperations) (Config, error) {
	return loadForOS(ctx, opts, goos, ops)
}
