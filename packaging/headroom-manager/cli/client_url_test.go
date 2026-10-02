package cli

import "testing"

func TestUsageURLRejectsQueriesAndFragments(t *testing.T) {
	for _, base := range []string{"https://usage.example/base#section", "https://usage.example/base#", "https://usage.example/base?", "https://usage.example/base?key=value"} {
		t.Run(base, func(t *testing.T) {
			if got, err := usageURL(base); err == nil {
				t.Fatalf("accepted unsupported URL as %q", got)
			}
		})
	}
	for base, want := range map[string]string{
		"https://usage.example/base%23name":  "https://usage.example/base%23name/api/v1/usage",
		"https://usage.example/base%3Fname/": "https://usage.example/base%3Fname/api/v1/usage",
	} {
		if got, err := usageURL(base); err != nil || got != want {
			t.Fatalf("usageURL(%q) = %q, %v; want %q", base, got, err, want)
		}
	}
}
