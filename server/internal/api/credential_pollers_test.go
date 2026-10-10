package api_test

import (
	"context"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
)

func (c *fakeCache) UpdateCredentials(ctx context.Context, name string, update func() error) (poller.Entry, bool, error) {
	if err := update(); err != nil {
		return poller.Entry{}, true, err
	}
	return c.PollProvider(ctx, name)
}

func (t *blockingCredentialTransaction) UpdateCredentials(ctx context.Context, name string, update func() error) (poller.Entry, bool, error) {
	if err := update(); err != nil {
		return poller.Entry{}, true, err
	}
	return t.PollProvider(ctx, name)
}
