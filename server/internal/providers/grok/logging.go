package grok

import (
	"errors"
	"log/slog"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/credstore"
)

func (p *Provider) recordRefusal(err error, spent credstore.TokenPair) {
	var refused *credstore.NotWritableError
	if !errors.As(err, &refused) {
		return
	}
	p.refusalLog.Record(p.logger, p.credentialsPath, refused.Reason, spent)
}

// warnUnsaveable reports a refresh that obtained tokens but could not write
// them. It is not deduplicated: the sign-in must be replaced.
func (p *Provider) warnUnsaveable(err error) {
	reason := ""
	var refused *credstore.NotWritableError
	if errors.As(err, &refused) {
		reason = string(refused.Reason)
	}
	p.logger.Warn("refreshed sign-in could not be saved",
		slog.String("path", p.credentialsPath),
		slog.String("reason", reason),
	)
}
