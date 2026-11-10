package cli

import (
	"encoding/json"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ProviderAccount struct {
	ID    string
	Label string
}

func (p Provider) Type() string {
	if p.ProviderType != "" {
		return p.ProviderType
	}
	return p.ProviderName
}

func groupProvidersByType(providers []Provider) []Provider {
	first := make(map[string]int, len(providers))
	for index, provider := range providers {
		if _, exists := first[provider.Type()]; !exists {
			first[provider.Type()] = index
		}
	}
	grouped := slices.Clone(providers)
	sort.SliceStable(grouped, func(i, j int) bool {
		return first[grouped[i].Type()] < first[grouped[j].Type()]
	})
	return grouped
}

func decodeProviderType(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil || utf8.RuneCountInString(value) > 32 {
		return ""
	}
	return value
}

var providerAccountID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

func decodeProviderAccount(raw json.RawMessage) *ProviderAccount {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil
	}
	var id, label string
	if json.Unmarshal(fields["id"], &id) != nil || !providerAccountID.MatchString(id) ||
		json.Unmarshal(fields["label"], &label) != nil {
		return nil
	}
	label = strings.TrimSpace(label)
	if count := utf8.RuneCountInString(label); count < 1 || count > 32 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return nil
	}
	return &ProviderAccount{ID: id, Label: label}
}
