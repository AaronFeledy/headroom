package sshaccess

import "testing"

func Test_Refresh_allowlist_accepts_only_exact_empty_POST(t *testing.T) {
	// Given
	cases := []struct {
		method, path string
		body         []byte
		allowed      bool
	}{
		{"POST", "/api/v1/usage/refresh", nil, true},
		{"POST", "/api/v1/usage/refresh", []byte("{}"), false},
		{"GET", "/api/v1/usage/refresh", nil, false},
		{"POST", "/api/v1/usage/refresh?x=1", nil, false},
	}
	for _, tc := range cases {
		// When
		got := allowedRequest(requestFrame{Method: tc.method, Path: tc.path, Body: tc.body})
		// Then
		if got != tc.allowed {
			t.Errorf("%s %s allowed = %v", tc.method, tc.path, got)
		}
	}
}
