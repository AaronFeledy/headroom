package cursor

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

type exactMinor struct {
	value   *int64
	invalid bool
}

func (v *exactMinor) UnmarshalJSON(raw []byte) error {
	*v = exactMinor{}
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	s := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			v.invalid = true
			return nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > usage.MaxSafeInteger {
		v.invalid = true
		return nil
	}
	v.value = &n
	return nil
}

type epochMillis struct {
	value   *time.Time
	invalid bool
}

func (v *epochMillis) UnmarshalJSON(raw []byte) error {
	*v = epochMillis{}
	if bytes.Equal(raw, []byte("null")) {
		return nil
	}
	s := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &s); err != nil {
			v.invalid = true
			return nil
		}
		if parsed, err := time.Parse(time.RFC3339Nano, s); err == nil {
			utc := parsed.UTC()
			v.value = &utc
			return nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 || n > 253402300799999 {
		v.invalid = true
		return nil
	}
	parsed := time.UnixMilli(n).UTC()
	v.value = &parsed
	return nil
}

var privateLabel = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://|www\.|@|[0-9a-f]{8}-[0-9a-f]{4}-|\b(user|auth0|org|team)[_:/|][a-z0-9]|\buser[0-9]+|[a-z0-9_-]{24,})`)

func safeLabel(raw string) *string {
	if utf8.RuneCountInString(raw) > 80 || !utf8.ValidString(raw) || strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 || privateLabel.MatchString(raw) {
		return nil
	}
	label := strings.TrimSpace(raw)
	if label == "" {
		return nil
	}
	return &label
}
