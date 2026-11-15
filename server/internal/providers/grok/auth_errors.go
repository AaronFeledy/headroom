package grok

import (
	"context"
	"errors"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func (p *Provider) authError(data usage.UsageData, err error) (usage.UsageData, error) {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return data, err
	}
	if errors.Is(err, errMissingAuthEntry) {
		return reauth(data), nil
	}
	data.Error = strPtr("Grok auth error. Run `grok login`.")
	return data, nil
}

func (p *Provider) refreshError(data usage.UsageData, err error) (usage.UsageData, error) {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureOther}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return data, err
	}
	if errors.Is(err, errInvalidGrant) {
		return reauth(data), nil
	}
	data.Error = strPtr("Token refresh failed. Will retry.")
	return data, nil
}

func reauth(data usage.UsageData) usage.UsageData {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureAuth}
	data.Error = strPtr("Grok auth expired. Run `grok login` again.")
	data.NeedsReauth = true
	return data
}

func unwritableReauth(data usage.UsageData) usage.UsageData {
	data.FetchFailure = &usage.FetchFailure{Kind: usage.FailureAuth}
	data.Error = strPtr("Sign-in expired. Headroom can't save a refreshed sign-in; run `grok login` to refresh it.")
	data.NeedsReauth = true
	return data
}
