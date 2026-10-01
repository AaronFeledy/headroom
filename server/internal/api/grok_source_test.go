package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/api"
)

func Test_GrokCredentials_source_name_remains_an_unknown_field(t *testing.T) {
	// Given
	cache := newFakeCache(entry("Grok", 0, 0, nil))
	handler := api.NewHandler(api.Options{Cache: cache, Grok: &fakeGrok{}, Poller: cache, ProviderNames: []string{"Grok"}})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/providers/grok/credentials", strings.NewReader(`{"cookie":"fixture","source_name":"Firefox"}`))
	recorder := httptest.NewRecorder()
	// When
	handler.ServeHTTP(recorder, request)
	// Then
	assertStatus(t, recorder, http.StatusBadRequest)
	assertJSON(t, recorder, `{"error":"invalid JSON"}`)
}
