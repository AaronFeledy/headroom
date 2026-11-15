package claude

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func mkdirLock(path string, staleAge time.Duration, allowStale bool) (os.FileInfo, error) {
	err := os.Mkdir(path, claudeCodeLockMode)
	if err == nil {
		info, claimErr := claimLockDir(path)
		if claimErr != nil {
			return nil, claimErr
		}
		return info, nil
	}
	if isLockPermissionError(err) {
		return nil, credstore.CreationRefusal(path, err)
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	info, lerr := os.Lstat(path)
	if errors.Is(lerr, os.ErrNotExist) {
		if !allowStale {
			return nil, errLockBusy
		}
		return mkdirLock(path, staleAge, false)
	}
	if lerr != nil {
		if isLockPermissionError(lerr) {
			return nil, credstore.CreationRefusal(path, lerr)
		}
		return nil, lerr
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errLockBusy
	}
	if !allowStale {
		return nil, errLockBusy
	}
	if time.Since(info.ModTime()) <= staleAge {
		return nil, errLockBusy
	}
	latest, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return mkdirLock(path, staleAge, false)
	}
	if err != nil {
		return nil, err
	}
	if !latest.IsDir() || !os.SameFile(info, latest) || !info.ModTime().Equal(latest.ModTime()) {
		return nil, errLockBusy
	}
	if remErr := removeLockDir(path); remErr != nil && !errors.Is(remErr, os.ErrNotExist) {
		if isLockPermissionError(remErr) {
			return nil, credstore.CreationRefusal(path, remErr)
		}
		// Nonempty or foreign type: treat as busy.
		return nil, errLockBusy
	}
	return mkdirLock(path, staleAge, false)
}

func claimLockDir(path string) (claimed os.FileInfo, err error) {
	initial, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !initial.IsDir() || initial.Mode()&os.ModeSymlink != 0 {
		return nil, errLockBusy
	}
	defer func() {
		if err != nil {
			current, statErr := os.Lstat(path)
			if errors.Is(statErr, os.ErrNotExist) {
				return
			}
			if statErr != nil {
				err = errors.Join(err, statErr)
			} else if current.IsDir() && os.SameFile(initial, current) {
				err = errors.Join(err, removeLockDir(path))
			}
		}
	}()
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := os.Chtimes(path, now, now); err != nil {
		if isLockPermissionError(err) {
			return nil, credstore.CreationRefusal(path, err)
		}
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(initial, info) {
		return nil, errLockBusy
	}
	mtime := info.ModTime().UTC()
	if !mtime.Equal(now) && !mtime.Equal(now.Truncate(time.Second)) {
		return nil, fmt.Errorf("claude: lock mtime not owned")
	}
	return info, nil
}

func (c claudeCodeLockConfig) waitBetweenAttempts(ctx context.Context, attempt int) error {
	if c.waitHook != nil {
		c.waitHook(ctx, attempt)
	}
	wait := c.retryWait
	if c.jitter {
		wait = defaultLockRetryWait + time.Duration(rand.Int63n(int64(defaultLockRetryWait)))
	}
	if wait <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (l *claudeCodeLock) startHeartbeat() {
	if l.cfg.heartbeatInterval <= 0 {
		return
	}
	l.stopCh = make(chan struct{})
	l.doneCh = make(chan struct{})
	go func() {
		defer close(l.doneCh)
		ticker := time.NewTicker(l.cfg.heartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-l.stopCh:
				return
			case <-ticker.C:
				if err := l.heartbeatStep(); err != nil {
					return
				}
			}
		}
	}()
}

func (l *claudeCodeLock) stopHeartbeat() {
	if l.stopCh == nil {
		return
	}
	close(l.stopCh)
	<-l.doneCh
	l.stopCh = nil
	l.doneCh = nil
}

func (l *claudeCodeLock) isLost() bool {
	return l.lost.Load()
}

// heartbeatStep verifies each held lock directory is still ours and renews its
// mtime. Ownership loss is sticky. Tests drive this directly instead of sleeping
// for the heartbeat interval.
func (l *claudeCodeLock) heartbeatStep() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lost.Load() {
		return errClaudeCodeLockLost
	}
	now := l.cfg.now().UTC().Truncate(time.Millisecond)
	for i := range l.dirs {
		d := &l.dirs[i]
		info, err := os.Lstat(d.path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(d.info, info) || !info.ModTime().UTC().Equal(d.mtime) {
			l.lost.Store(true)
			return errClaudeCodeLockLost
		}
		if err := os.Chtimes(d.path, now, now); err != nil {
			l.lost.Store(true)
			return errClaudeCodeLockLost
		}
		renewed, err := os.Lstat(d.path)
		if err != nil || !renewed.IsDir() || !os.SameFile(d.info, renewed) {
			l.lost.Store(true)
			return errClaudeCodeLockLost
		}
		mtime := renewed.ModTime().UTC()
		if !mtime.Equal(now) && !mtime.Equal(now.Truncate(time.Second)) {
			l.lost.Store(true)
			return errClaudeCodeLockLost
		}
		d.info = renewed
		d.mtime = mtime
	}
	return nil
}

func (l *claudeCodeLock) release() error {
	l.stopHeartbeat()
	l.mu.Lock()
	defer l.mu.Unlock()
	err := releaseHeldDirs(l.dirs)
	l.dirs = nil
	return err
}

func releaseHeldDirs(dirs []heldLockDir) (releaseErr error) {
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		info, err := os.Lstat(d.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			releaseErr = errors.Join(releaseErr, err)
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(d.info, info) || !info.ModTime().UTC().Equal(d.mtime) {
			continue
		}
		if err := removeLockDir(d.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			releaseErr = errors.Join(releaseErr, err)
		}
	}
	return releaseErr
}
