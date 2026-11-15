package claude

import (
	"errors"
	"log/slog"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func (c *Client) recordRefusal(path string, err error, spent credstore.TokenPair) {
	var refused *credstore.NotWritableError
	if !errors.As(err, &refused) {
		return
	}
	c.refusalLog.Record(c.logger, path, refused.Reason, spent)
}

// warnUnsaveable reports a refresh that obtained tokens but could not write
// them. It is not deduplicated: the sign-in must be replaced.
func (c *Client) warnUnsaveable(path string, err error) {
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
