//go:build linux

package sshaccess

import "testing"

func Test_Automatic_reset_allowlist_accepts_only_the_expected_shapes(t *testing.T) {
	// Given
	tests := []struct {
		name   string
		method string
		path   string
		body   []byte
		want   bool
	}{
		{"codex arm", "PUT", "/api/v1/providers/codex/reset/automatic", []byte(`{}`), true},
		{"claude arm", "PUT", "/api/v1/providers/claude/reset/automatic", []byte(`{}`), true},
		{"codex cancel", "DELETE", "/api/v1/providers/codex/reset/automatic", nil, true},
		{"claude cancel", "DELETE", "/api/v1/providers/claude/reset/automatic", nil, true},
		{"cancel with body", "DELETE", "/api/v1/providers/claude/reset/automatic", []byte(`{}`), false},
		{"unrelated method", "POST", "/api/v1/providers/claude/reset/automatic", nil, false},
		{"manual reset unchanged", "POST", "/api/v1/providers/claude/reset", []byte(`{}`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			request := requestFrame{Schema: 1, Method: tt.method, Path: tt.path, Body: tt.body}

			// When
			got := allowedRequest(request)

			// Then
			if got != tt.want {
				t.Fatalf("allowedRequest(%s %s, %d bytes) = %v, want %v", tt.method, tt.path, len(tt.body), got, tt.want)
			}
		})
	}
}
