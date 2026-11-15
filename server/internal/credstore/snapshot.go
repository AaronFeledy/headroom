package credstore

import (
	"context"
	"errors"
	"fmt"
	"io"
)

func LoadSnapshot(ctx context.Context, path string) (snapshot Snapshot, err error) {
	select {
	case <-ctx.Done():
		return Snapshot{}, fmt.Errorf("load snapshot canceled: %w", ctx.Err())
	default:
	}
	file, err := openSnapshot(path)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open credential snapshot %s: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close credential snapshot %s: %w", path, closeErr))
		}
	}()
	select {
	case <-ctx.Done():
		return Snapshot{}, fmt.Errorf("load snapshot canceled: %w", ctx.Err())
	default:
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read credential snapshot %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return Snapshot{}, fmt.Errorf("stat credential snapshot %s: %w", path, err)
	}
	return Snapshot{Data: data, ModTime: info.ModTime(), Mode: info.Mode().Perm()}, nil
}
