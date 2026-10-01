package poller

import (
	"context"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

func Test_Poller_error_overlay_preserves_current_auth_not_last_good(t *testing.T) {
	// Given
	message := "fixture expired"
	provider := &sequenceProvider{name: "Cursor", responses: []providerResult{
		{data: usage.UsageData{ProviderName: "Cursor", Auth: usage.NewAuth("Cursor", "signed_in", &usage.AuthSource{Kind: "cli", Name: "cursor-agent"})}},
		{data: usage.UsageData{ProviderName: "Cursor", Auth: usage.NewAuth("Cursor", "expired", &usage.AuthSource{Kind: "desktop", Name: "Firefox"}), Error: &message}},
	}}
	poller := New(Options{})
	if err := poller.Register(provider, true); err != nil {
		t.Fatal(err)
	}
	poller.PollAll(context.Background())
	// When
	data := poller.PollAll(context.Background())[0].Data
	// Then
	if data.Auth.State != "expired" || data.Auth.Source == nil || data.Auth.Source.Name != "Firefox" {
		t.Fatal(data.Auth)
	}
}
