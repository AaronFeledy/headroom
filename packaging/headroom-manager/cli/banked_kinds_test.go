package cli

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

const bankedKindFingerprint = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func bankedKindBody(credits string) []byte {
	return []byte(`[{"provider_name":"Claude","error":null,"is_success":true,"needs_reauth":false,"buckets":[{"id":"weekly","label":"Weekly","utilization":40,"resets_at":null}],"rate_limit_reset_credits":` + credits + `}]`)
}

func bankedKindSnapshot(t *testing.T, top int, kindsJSON string) (Provider, string) {
	t.Helper()
	credits := `{"available_count":` + strconv.Itoa(top) + `,"account_fingerprint":"` + bankedKindFingerprint + `","usable_now":true`
	if kindsJSON != "" {
		credits += `,"kinds":` + kindsJSON
	}
	providers, err := decodeUsage(bankedKindBody(credits + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(providers))
	}
	return providers[0], render(providers, time.Unix(0, 0), false, map[string]warningState{})
}

func TestBankedKindBreakdownListsKinds(t *testing.T) {
	// Given
	provider, text := bankedKindSnapshot(t, 3, `[
		{"id":"weekly","label":"weekly reset","available_count":1,"usable_now":true,"refills":["weekly","weekly_fable"],"gives_back_weekly":false,"expires_at":"2026-12-10T22:00:00Z"},
		{"id":"fable","label":"Fable reset","available_count":2,"usable_now":true,"refills":["weekly_fable"],"gives_back_weekly":true,"expires_at":"2027-03-09T07:59:00Z"}]`)
	// Then
	if !strings.Contains(text, "  3 banked resets (1 weekly, 2 Fable)\n") {
		t.Fatalf("missing kind breakdown: %s", text)
	}
	credits := provider.RateLimitResetCredits
	if credits == nil || len(credits.Kinds) != 2 || credits.Kinds[0].ID != "weekly" ||
		credits.Kinds[1].Label != "Fable reset" || credits.Kinds[1].AvailableCount != 2 {
		t.Fatalf("kinds were not parsed: %#v", credits)
	}
}

func TestBankedKindBreakdownSingleKind(t *testing.T) {
	for _, test := range []struct {
		name, label, want string
		count             int
	}{
		{"plural total", "Fable reset", "  2 banked resets (Fable)\n", 2},
		{"singular total", "5-hour reset", "  1 banked reset (5-hour)\n", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given / When
			_, text := bankedKindSnapshot(t, test.count, `[{"id":"fable","label":"`+test.label+
				`","available_count":`+strconv.Itoa(test.count)+`}]`)
			// Then
			if !strings.Contains(text, test.want) {
				t.Fatalf("missing %q in %s", test.want, text)
			}
		})
	}
}

func TestBankedKindBreakdownFallbacks(t *testing.T) {
	many := make([]string, 9)
	for index := range many {
		many[index] = fmt.Sprintf(`{"id":"kind_%d","label":"reset %d","available_count":0}`, index, index)
	}
	for _, test := range []struct {
		name   string
		top    int
		kinds  string
		parsed int
	}{
		{"sum mismatch", 3, `[{"id":"weekly","label":"weekly reset","available_count":1},` +
			`{"id":"fable","label":"Fable reset","available_count":1}]`, 2},
		{"duplicate id", 2, `[{"id":"weekly","label":"weekly reset","available_count":1},` +
			`{"id":"weekly","label":"weekly reset","available_count":1}]`, 0},
		{"uppercase id", 1, `[{"id":"Weekly","label":"weekly reset","available_count":1}]`, 0},
		{"id too long", 1, `[{"id":"` + strings.Repeat("a", 33) + `","label":"weekly reset","available_count":1}]`, 0},
		{"empty label", 1, `[{"id":"weekly","label":"","available_count":1}]`, 0},
		{"control label", 1, `[{"id":"weekly","label":"weekly\u0007reset","available_count":1}]`, 0},
		{"label too long", 1, `[{"id":"weekly","label":"` + strings.Repeat("a", 65) + `","available_count":1}]`, 0},
		{"negative count", 1, `[{"id":"weekly","label":"weekly reset","available_count":-1}]`, 0},
		{"fractional count", 1, `[{"id":"weekly","label":"weekly reset","available_count":1.5}]`, 0},
		{"count too large", 1, `[{"id":"weekly","label":"weekly reset","available_count":9007199254740992}]`, 0},
		{"entry not an object", 1, `[1]`, 0},
		{"too many entries", 1, `[` + strings.Join(many, ",") + `]`, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given / When
			provider, text := bankedKindSnapshot(t, test.top, test.kinds)
			// Then
			today := fmt.Sprintf("  %d banked reset", test.top)
			if test.top != 1 {
				today += "s"
			}
			if !strings.Contains(text, today+"\n") || strings.Contains(text, "banked reset (") || strings.Contains(text, "banked resets (") {
				t.Fatalf("expected today's text, got: %s", text)
			}
			credits := provider.RateLimitResetCredits
			if credits == nil || credits.AvailableCount != int64(test.top) || credits.AccountFingerprint == nil ||
				*credits.AccountFingerprint != bankedKindFingerprint {
				t.Fatalf("invalid kinds disturbed the rest: %#v", credits)
			}
			if test.parsed == 0 {
				if credits.Kinds != nil {
					t.Fatalf("invalid kinds were kept: %#v", credits.Kinds)
				}
			} else if len(credits.Kinds) != test.parsed {
				t.Fatalf("kinds = %#v, want %d entries", credits.Kinds, test.parsed)
			}
		})
	}
}

func TestBankedKindMetadataUnknown(t *testing.T) {
	for _, test := range []struct {
		name  string
		kinds string
	}{
		{"absent", ""},
		{"null", `null`},
		{"object", `{}`},
		{"string", `"weekly"`},
		{"number", `7`},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Given / When
			provider, text := bankedKindSnapshot(t, 3, test.kinds)
			// Then
			if !strings.Contains(text, "  3 banked resets\n") || strings.Contains(text, "banked resets (") {
				t.Fatalf("unknown kinds changed the text: %s", text)
			}
			if credits := provider.RateLimitResetCredits; credits == nil || credits.Kinds != nil {
				t.Fatalf("unknown kinds were kept: %#v", credits)
			}
		})
	}
}

func TestBankedKindBreakdownSkipsZeroCounts(t *testing.T) {
	// Given / When
	_, text := bankedKindSnapshot(t, 2, `[{"id":"weekly","label":"weekly reset","available_count":0},`+
		`{"id":"fable","label":"Fable reset","available_count":2}]`)
	// Then
	if !strings.Contains(text, "  2 banked resets (Fable)\n") {
		t.Fatalf("zero-count kind was listed: %s", text)
	}
}

func TestJSONKeepsBankedKindsByteForByte(t *testing.T) {
	// Given
	body := bankedKindBody(`{"available_count":3,"account_fingerprint":"` + bankedKindFingerprint +
		`","usable_now":true,"kinds":[{"id":"weekly","label":"weekly reset","available_count":1,"usable_now":true},` +
		`{"id":"fable","label":"Fable reset","available_count":2,"usable_now":true}]}`)
	options, stdout, _ := baseOptions(body)
	options.Args = []string{"--once", "--json"}
	// When
	if err := Run(context.Background(), options); err != nil {
		t.Fatal(err)
	}
	// Then
	if stdout.String() != string(body)+"\n" {
		t.Fatalf("JSON output changed bytes:\n%s", stdout.String())
	}
}

func TestWeeklyOnlyKeepsBankedKinds(t *testing.T) {
	// Given
	body := bankedKindBody(`{"available_count":3,"account_fingerprint":"` + bankedKindFingerprint +
		`","usable_now":true,"kinds":[{"id":"weekly","label":"weekly reset","available_count":1,"usable_now":true},` +
		`{"id":"fable","label":"Fable reset","available_count":2,"usable_now":true}]}`)
	options, _, _ := baseOptions(body)
	// When
	snapshot, providers, err := fetchUsage(context.Background(), options.defaults(), dashboardFlags{weeklyOnly: true})
	// Then
	if err != nil {
		t.Fatal(err)
	}
	if len(providers[0].Buckets) != 1 || providers[0].Buckets[0].ID != "weekly" {
		t.Fatalf("weekly filter changed: %#v", providers[0].Buckets)
	}
	want := `"kinds":[{"id":"weekly","label":"weekly reset","available_count":1,"usable_now":true},` +
		`{"id":"fable","label":"Fable reset","available_count":2,"usable_now":true}]`
	if !bytes.Contains(snapshot.Body, []byte(want)) {
		t.Fatalf("kinds did not pass through: %s", snapshot.Body)
	}
}
