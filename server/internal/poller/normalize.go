package poller

import (
	"context"
	"errors"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func (s *providerState) normalize(data usage.UsageData, err error) usage.UsageData {
	if data.ProviderName == "" {
		data.ProviderName = s.name
	}
	if err == nil && data.Error == nil {
		s.stampIdentity(&data)
		if s.hasLastGood && sameCredentialEpoch(s.lastGood.CredentialEpoch, data.CredentialEpoch) {
			data = keepWindowResets(s.lastGood, data)
		}
		s.lastGood = copyUsageData(data)
		s.hasLastGood = true
		return copyUsageData(data)
	}
	var out usage.UsageData
	if err == nil {
		out = s.overlayExpectedError(data)
	} else {
		out = s.overlayFetchFailure(data, err)
	}
	s.stampIdentity(&out)
	return out
}

func (s *providerState) overlayExpectedError(data usage.UsageData) usage.UsageData {
	if !s.hasLastGood {
		return copyUsageData(data)
	}
	overlay := copyUsageData(s.lastGood)
	overlay.Auth = copyAuth(data.Auth)
	overlay.Error = copyString(data.Error)
	overlay.NeedsReauth = data.NeedsReauth
	overlay.ReauthCommand = copyString(data.ReauthCommand)
	overlay.PrimaryStatusText = copyString(data.PrimaryStatusText)
	overlay.SecondaryStatusText = copyString(data.SecondaryStatusText)
	return overlay
}

func (s *providerState) overlayFetchFailure(data usage.UsageData, err error) usage.UsageData {
	message := "Provider fetch failed. Will retry."
	if usage.IsTimeout(err) {
		message = "Provider fetch timed out. Will retry."
	} else if errors.Is(err, context.Canceled) {
		message = "Provider fetch canceled."
	}
	if s.hasLastGood {
		data = overlayFetchFailureFields(copyUsageData(s.lastGood), data)
	} else {
		data = copyUsageData(data)
	}
	if data.ProviderName == "" {
		data.ProviderName = s.name
	}
	data.Error = &message
	return copyUsageData(data)
}

func overlayFetchFailureFields(base usage.UsageData, partial usage.UsageData) usage.UsageData {
	base.Auth = copyAuth(partial.Auth)
	base.NeedsReauth = partial.NeedsReauth
	base.ReauthCommand = copyString(partial.ReauthCommand)
	base.PrimaryStatusText = copyString(partial.PrimaryStatusText)
	base.SecondaryStatusText = copyString(partial.SecondaryStatusText)
	return base
}
