package claude

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

const (
	claudeCodeLockName   = ".oauth_refresh.lock"
	claudeCodeLockMode   = 0o777
	defaultLockAttempts  = 5
	defaultLockStale     = 60 * time.Second
	defaultLockHeartbeat = 5 * time.Second
	defaultLockRetryWait = time.Second
	tokenRequestTimeout  = 30 * time.Second
)

// claudeCodeLockConfig controls Claude Code's mkdir lock. Production uses
// jittered 1-2s waits and a 5s heartbeat. Tests set retryWait and
// heartbeatInterval to 0 (no sleep / no background heartbeat) and inject
// waitHook to simulate another CLI finishing under the lock.
type claudeCodeLockConfig struct {
	maxAttempts       int
	retryWait         time.Duration
	jitter            bool
	heartbeatInterval time.Duration
	staleAge          time.Duration
	waitHook          func(context.Context, int)
	afterAcquire      func(*claudeCodeLock)
	now               func() time.Time
}

func defaultClaudeCodeLockConfig() claudeCodeLockConfig {
	return claudeCodeLockConfig{
		maxAttempts:       defaultLockAttempts,
		retryWait:         defaultLockRetryWait,
		jitter:            true,
		heartbeatInterval: defaultLockHeartbeat,
		staleAge:          defaultLockStale,
	}
}

func (c claudeCodeLockConfig) normalized() claudeCodeLockConfig {
	if c.maxAttempts <= 0 {
		c.maxAttempts = defaultLockAttempts
	}
	if c.staleAge <= 0 {
		c.staleAge = defaultLockStale
	}
	if c.now == nil {
		c.now = time.Now
	}
	return c
}

type heldLockDir struct {
	path  string
	info  os.FileInfo
	mtime time.Time
}

type claudeCodeLock struct {
	dirs   []heldLockDir
	lost   atomic.Bool
	cfg    claudeCodeLockConfig
	mu     sync.Mutex
	stopCh chan struct{}
	doneCh chan struct{}
}

// acquireClaudeCodeLock mirrors Claude Code 2.1.325: mkdir
// <dir>/.oauth_refresh.lock (realpath:false), then the legacy
// <EvalSymlinks(dir)|dir>.lock. Legacy ELOCKED releases the new lock and
// retries. Headroom writes no .oauth_refresh.lock.owner record, so Claude
// Code's dead-holder takeover does not apply; ordinary 60s stale reclamation
// is prevented only by the heartbeat.
func acquireClaudeCodeLock(ctx context.Context, dir string, cfg claudeCodeLockConfig) (*claudeCodeLock, error) {
	cfg = cfg.normalized()
	if dir == "" {
		return nil, fmt.Errorf("claude code lock: empty directory")
	}
	newPath := filepath.Join(dir, claudeCodeLockName)
	legacyPath := legacyClaudeCodeLockPath(dir)

	for attempt := 1; attempt <= cfg.maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		newInfo, err := mkdirLock(newPath, cfg.staleAge, true)
		if err != nil {
			if errors.Is(err, credstore.ErrNotWritable) {
				return nil, err
			}
			if !errors.Is(err, errLockBusy) {
				return nil, err
			}
			if attempt >= cfg.maxAttempts {
				return nil, errClaudeCodeRefreshing
			}
			if waitErr := cfg.waitBetweenAttempts(ctx, attempt); waitErr != nil {
				return nil, waitErr
			}
			continue
		}
		held := []heldLockDir{{path: newPath, info: newInfo, mtime: newInfo.ModTime().UTC()}}

		legInfo, legErr := mkdirLock(legacyPath, cfg.staleAge, true)
		if legErr != nil {
			if errors.Is(legErr, errLockBusy) {
				if err := releaseHeldDirs(held); err != nil {
					return nil, err
				}
				if attempt >= cfg.maxAttempts {
					return nil, errClaudeCodeRefreshing
				}
				if waitErr := cfg.waitBetweenAttempts(ctx, attempt); waitErr != nil {
					return nil, waitErr
				}
				continue
			}
			// Other legacy failures (including unwritable): keep only the new lock,
			// matching Claude Code when legacy fails for a non-ELOCKED reason.
		} else {
			held = append(held, heldLockDir{path: legacyPath, info: legInfo, mtime: legInfo.ModTime().UTC()})
		}

		lock := &claudeCodeLock{dirs: held, cfg: cfg}
		lock.startHeartbeat()
		if cfg.afterAcquire != nil {
			cfg.afterAcquire(lock)
		}
		return lock, nil
	}
	return nil, errClaudeCodeRefreshing
}

func legacyClaudeCodeLockPath(dir string) string {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		resolved = dir
	}
	return resolved + ".lock"
}

var errLockBusy = errors.New("claude: oauth refresh lock busy")
