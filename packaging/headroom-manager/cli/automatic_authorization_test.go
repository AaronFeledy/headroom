package cli

import (
	"strings"
	"testing"
	"time"
)

func automaticAuthorizationSnapshot(t *testing.T, automation string) string {
	t.Helper()
	body := []byte(`[{"provider_name":"Codex","error":null,"is_success":true,"needs_reauth":false,` +
		`"buckets":[{"id":"weekly","label":"Weekly","utilization":95,"resets_at":"2026-11-13T12:00:00Z"}],` +
		`"rate_limit_reset_credits":{"available_count":1,"account_fingerprint":"` + bankedKindFingerprint + `","usable_now":null},` +
		`"reset_automation":` + automation + `}]`)
	providers, err := decodeUsage(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].ResetAutomation == nil || !providers[0].ResetAutomation.Armed {
		t.Fatalf("automatic state was not parsed: %#v", providers)
	}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	return render(providers, now, false, map[string]warningState{})
}

func Test_Automatic_authorization_line_shows_the_window_countdown(t *testing.T) {
	// When
	text := automaticAuthorizationSnapshot(t, `{"armed":true,"window_ends_at":"2026-11-13T12:00:00Z"}`)

	// Then
	if !strings.Contains(text, "  1 banked reset\n  automatic reset set until Resets in 2d 0h\n") {
		t.Fatalf("missing automatic reset line: %s", text)
	}
}

func Test_Automatic_authorization_line_falls_back_without_a_time(t *testing.T) {
	for _, value := range []string{"null", `"tomorrow"`, "42"} {
		t.Run(value, func(t *testing.T) {
			// When
			text := automaticAuthorizationSnapshot(t, `{"armed":true,"window_ends_at":`+value+`}`)

			// Then
			if !strings.Contains(text, "\n  automatic reset set\n") || strings.Contains(text, "automatic reset set until") {
				t.Fatalf("wrong fallback line: %s", text)
			}
		})
	}
}

func Test_Automatic_authorization_line_is_absent_when_disarmed(t *testing.T) {
	// Given
	body := []byte(`[{"provider_name":"Cursor","error":null,"is_success":true,"needs_reauth":false,` +
		`"buckets":[{"id":"weekly","label":"Weekly","utilization":40,"resets_at":null}],` +
		`"rate_limit_reset_credits":null,"reset_automation":{"armed":false}}]`)
	providers, err := decodeUsage(body)
	if err != nil {
		t.Fatal(err)
	}

	// When
	text := render(providers, time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC), false, map[string]warningState{})

	// Then
	if strings.Contains(text, "automatic reset set") {
		t.Fatalf("disarmed state printed a line: %s", text)
	}
}
