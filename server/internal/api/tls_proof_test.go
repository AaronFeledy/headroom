package api_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
)

func Test_TLSProof_bypasses_bearer_and_matches_exact_HMAC(t *testing.T) {
	// Given
	fingerprint := strings.Repeat("a", 64)
	nonce := strings.Repeat("AB", 32)
	handler := api.NewHandler(api.Options{AuthToken: "fixture-token", TLSCertificateSHA256: fingerprint})
	request := httptest.NewRequest("GET", "/api/v1/tls/proof?nonce="+nonce, nil)
	request.TLS = &tls.ConnectionState{}
	recorder := httptest.NewRecorder()
	// When
	handler.ServeHTTP(recorder, request)
	// Then
	if recorder.Code != 200 || recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(recorder.Code, recorder.Header())
	}
	var response struct {
		CertificateSHA256 string `json:"certificate_sha256"`
		Proof             string `json:"proof"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("fixture-token"))
	mac.Write([]byte("headroom-tls-proof-v1\n" + strings.ToLower(nonce) + "\n" + fingerprint))
	if response.CertificateSHA256 != fingerprint || response.Proof != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatal(response)
	}
}

func Test_TLSProof_unavailable_or_invalid_and_other_routes_require_bearer(t *testing.T) {
	// Given
	for _, tt := range []struct {
		path, token string
		tls         bool
		want        int
	}{
		{"/api/v1/tls/proof?nonce=" + strings.Repeat("a", 64), "fixture", false, 404},
		{"/api/v1/tls/proof?nonce=" + strings.Repeat("a", 64), "", true, 404},
		{"/api/v1/tls/proof?nonce=bad", "fixture", true, 400},
		{"/api/v1/health", "fixture", true, 401},
	} {
		t.Run(tt.path+tt.token, func(t *testing.T) {
			handler := api.NewHandler(api.Options{AuthToken: tt.token, TLSCertificateSHA256: strings.Repeat("a", 64)})
			request := httptest.NewRequest("GET", tt.path, nil)
			if tt.tls {
				request.TLS = &tls.ConnectionState{}
			}
			recorder := httptest.NewRecorder()
			// When
			handler.ServeHTTP(recorder, request)
			// Then
			if recorder.Code != tt.want {
				t.Fatal(recorder.Code)
			}
		})
	}
}

func Test_CursorCredentials_source_name_validation(t *testing.T) {
	// Given
	for _, tt := range []struct {
		body string
		want int
	}{
		{`{"cookie":"fixture","source_name":" Firefox "}`, 200},
		{`{"cookie":"fixture","source_name":""}`, 400},
		{`{"cookie":"fixture","source_name":"\u0001"}`, 400},
		{`{"cookie":"fixture","source_name":null}`, 400},
		{`{"cookie":"fixture","source_name":"` + strings.Repeat("x", 41) + `"}`, 400},
		{`{"access_token":"fixture","source_name":"Firefox"}`, 400},
	} {
		t.Run(tt.body, func(t *testing.T) {
			cache := newFakeCache(entry("Cursor", 0, 0, nil))
			handler := api.NewHandler(api.Options{Cursor: &fakeCursor{}, Poller: cache, Cache: cache, ProviderNames: []string{"Cursor"}})
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPut, "/api/v1/providers/cursor/credentials", strings.NewReader(tt.body))
			// When
			handler.ServeHTTP(recorder, request)
			// Then
			if recorder.Code != tt.want {
				t.Fatal(recorder.Code)
			}
		})
	}
}
