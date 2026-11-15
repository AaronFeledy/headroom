package codex

import (
	"errors"
	"log/slog"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func (c *Client) recordRefusal(err error, spent Credentials) {
	var refused *credstore.NotWritableError
	if !errors.As(err, &refused) {
		return
	}
	c.store.mu.RLock()
	path := c.store.path
	c.store.mu.RUnlock()
	c.refusalLog.Record(c.logger, path, refused.Reason, tokenPair(spent))
}

// warnUnsaveable reports a refresh that obtained tokens but could not write
// them. It is not deduplicated: the sign-in must be replaced.
func (c *Client) warnUnsaveable(err error) {
	c.store.mu.RLock()
	path := c.store.path
	c.store.mu.RUnlock()
	reason := ""
	var refused *credstore.NotWritableError
	if errors.As(err, &refused) {
		reason = string(refused.Reason)
	}
	c.logger.Warn("refreshed sign-in could not be saved",
		slog.String("path", path),
		slog.String("reason", reason),
	)
}
