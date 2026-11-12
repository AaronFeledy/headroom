package claude

import (
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const nextGrantLabelLimit = 160

var errResetKindAmbiguous = errors.New("multiple reset kinds available")
var errResetKindUnavailable = errors.New("reset kind unavailable")

func nextGrantSummary(grant *bankGrant) *usage.ResetCreditNext {
	if grant == nil {
		return nil
	}
	next := &usage.ResetCreditNext{Label: grantLabel(grant.Label)}
	if grant.EndsAt != nil {
		if parsed, err := time.Parse(time.RFC3339, *grant.EndsAt); err == nil {
			formatted := parsed.UTC().Format(time.RFC3339)
			next.ExpiresAt = &formatted
		}
	}
	return next
}

// grantLabel keeps upstream wording safe for logs and single-line UI while
// preserving the text people see in Claude.
func grantLabel(label string) string {
	var builder strings.Builder
	for _, char := range label {
		if unicode.IsControl(char) {
			builder.WriteRune(' ')
			continue
		}
		builder.WriteRune(char)
	}
	runes := []rune(strings.TrimSpace(builder.String()))
	if len(runes) > nextGrantLabelLimit {
		runes = runes[:nextGrantLabelLimit]
	}
	return string(runes)
}

// selectResetGrant is the pure pre-submission choice over a parsed status.
// Kind-aware redemption requires an open, counted, usable, recognized grant of
// that kind. Legacy redemption keeps next_grant_id, but only when at most one
// kind has resets available; an unrecognized next grant is always a refusal.
func selectResetGrant(status *bankStatus, kind string, now time.Time) (*bankGrant, error) {
	if status == nil {
		return nil, errResetUnavailable
	}
	if kind == "" {
		kindsWithResets := 0
		for _, id := range resetKindOrder {
			if status.countKind(id) > 0 {
				kindsWithResets++
			}
		}
		if kindsWithResets > 1 {
			return nil, errResetKindAmbiguous
		}
		for index := range status.grants {
			grant := &status.grants[index]
			if grant.ID != status.nextGrantID {
				continue
			}
			if !grant.recognized() {
				return nil, errResetUnavailable
			}
			return grant, nil
		}
		return nil, errResetUnavailable
	}
	var candidates []*bankGrant
	for index := range status.grants {
		grant := &status.grants[index]
		if !grant.recognized() || grant.kind() != kind || !grant.counted() || !*grant.UsableNow || !grant.windowOpen(now) {
			continue
		}
		candidates = append(candidates, grant)
	}
	if len(candidates) == 0 {
		return nil, errResetKindUnavailable
	}
	for _, candidate := range candidates {
		if candidate.ID == status.nextGrantID {
			return candidate, nil
		}
	}
	return candidates[0], nil
}
