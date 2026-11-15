package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func Test_Client_refresh_updates_the_section_it_read(t *testing.T) {
	// Given: a Claude Code path holding only an OpenCode-shaped sign-in.
	home, path := claudeHomeLayout(t)
	writeFile(t, path, []byte(`{"anthropic":{"type":"oauth","access":"old","refresh":"old-refresh","expires":`+intString(pastMillis(t))+`},"openai":{"access":"keep"}}`))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			writeResponse(t, w, `{"access_token":"new","refresh_token":"new-refresh","expires_in":3600}`)
			return
		}
		writeResponse(t, w, `{"five_hour":{"utilization":1}}`)
	}))
	defer server.Close()
	client := New(Options{CredentialsPath: path, HomeDir: home, UsageURL: server.URL + "/usage", TokenURL: server.URL + "/token", HTTPClient: server.Client()})
	client.lockConfig = testLockConfig()

	// When
	got, err := client.Fetch(context.Background())

	// Then: the anthropic section carries the new pair, and no Claude section appears.
	if err != nil || got.Error != nil {
		t.Fatalf("fetch: %+v %v", got, err)
	}
	var file struct {
		Anthropic map[string]any  `json:"anthropic"`
		Claude    json.RawMessage `json:"claudeAiOauth"`
		OpenAI    map[string]any  `json:"openai"`
	}
	if err := json.Unmarshal([]byte(readFile(t, path)), &file); err != nil {
		t.Fatal(err)
	}
	if file.Anthropic["access"] != "new" || file.Anthropic["refresh"] != "new-refresh" || file.Anthropic["type"] != "oauth" {
		t.Fatalf("anthropic section = %v, want the refreshed pair", file.Anthropic)
	}
	if file.Claude != nil {
		t.Fatalf("refresh added a claudeAiOauth section: %s", file.Claude)
	}
	if file.OpenAI["access"] != "keep" {
		t.Fatalf("unrelated section changed: %v", file.OpenAI)
	}
}
