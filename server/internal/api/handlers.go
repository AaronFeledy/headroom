package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/resetauto"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const maxCredentialBodyBytes = 1 << 20
const maxResetBodyBytes = 1024
const resetOperationTimeout = 60 * time.Second
const resetWriteTimeout = 65 * time.Second

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var accountFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var resetKindPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

type handler struct {
	authToken      string
	tlsFingerprint string
	cache          Cache
	cursor         CursorCredentials
	grok           GrokCredentials
	codex          ProviderResetter
	claude         ProviderResetter
	poller         ProviderPoller
	version        string
	providerNames  []string
	credentialMu   sync.Mutex
	reset          *ResetCore
	automation     *resetauto.Automation
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/api/v1/tls/proof" {
		h.route(w, r, http.MethodGet, h.tlsProof)
		return
	}
	if path == "/api/v1/usage" {
		h.route(w, r, http.MethodGet, h.usageCollection)
		return
	}
	if path == "/api/v1/usage/refresh" {
		h.route(w, r, http.MethodPost, h.refreshUsage)
		return
	}
	if strings.HasPrefix(path, "/api/v1/usage/") && strings.TrimPrefix(path, "/api/v1/usage/") != "" {
		h.route(w, r, http.MethodGet, h.providerUsage)
		return
	}
	if path == "/api/v1/health" {
		h.route(w, r, http.MethodGet, h.health)
		return
	}
	if path == "/api/v1/providers/cursor/credentials" {
		h.route(w, r, http.MethodPut, h.cursorCredentials)
		return
	}
	if path == "/api/v1/providers/grok/credentials" {
		h.route(w, r, http.MethodPut, h.grokCredentials)
		return
	}
	if path == "/api/v1/providers/codex/reset" || path == "/api/v1/providers/claude/reset" {
		h.route(w, r, http.MethodPost, h.providerReset)
		return
	}
	if path == "/api/v1/providers/codex/reset/automatic" || path == "/api/v1/providers/claude/reset/automatic" {
		h.automaticResetRoute(w, r)
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

type codexResetRequest struct {
	RequestID          string `json:"request_id"`
	Confirmed          bool   `json:"confirmed"`
	AccountFingerprint string `json:"account_fingerprint"`
	// Kind is accepted only on the Claude route; Codex rejects that key.
	Kind        string `json:"kind"`
	kindPresent bool
}

type codexResetResponse struct {
	Outcome string `json:"outcome"`
}

// This handler can spend a very valuable banked reset. Do not test the reset
// button, this endpoint, or any code that may trigger a reset. The skipped
// safety tests are intentional and must not be converted into executable tests.
func (h *handler) providerReset(w http.ResponseWriter, r *http.Request) {
	provider := "codex"
	if r.URL.Path == "/api/v1/providers/claude/reset" {
		provider = "claude"
	}
	resetter := h.codex
	if provider == "claude" {
		resetter = h.claude
	}
	if resetter == nil || h.poller == nil || !h.hasProvider(provider) {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	started := time.Now()
	resetCtx, cancel := context.WithDeadline(r.Context(), started.Add(resetOperationTimeout))
	defer cancel()
	if err := http.NewResponseController(w).SetWriteDeadline(started.Add(resetWriteTimeout)); err != nil {
		writeError(w, http.StatusServiceUnavailable, "reset request unavailable")
		return
	}
	if len(r.Header.Values("Origin")) > 0 || len(r.Header.Values("Sec-Fetch-Site")) > 0 {
		writeError(w, http.StatusForbidden, "browser requests are not allowed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResetBodyBytes)
	defer r.Body.Close()
	request, err := decodeResetRequest(json.NewDecoder(r.Body), provider == "claude")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !request.Confirmed || !uuidPattern.MatchString(request.RequestID) || !accountFingerprintPattern.MatchString(request.AccountFingerprint) {
		writeError(w, http.StatusBadRequest, "request_id must be a UUID, confirmed must be true, and account_fingerprint must be lowercase SHA-256")
		return
	}
	// A manual reset replaces any armed automatic authorization. A cancel
	// failure never blocks the manual reset; this is a preference.
	h.automation.CancelManualReset(resetCtx, provider)
	result := h.reset.Execute(resetCtx, provider, ResetRequest{
		RequestID:          strings.ToLower(request.RequestID),
		AccountFingerprint: request.AccountFingerprint,
		Kind:               request.Kind,
	})
	if result.Status != 0 {
		writeError(w, result.Status, result.Message)
		return
	}
	writeJSON(w, http.StatusOK, codexResetResponse{Outcome: result.Outcome})
}

// The provider finalizer keeps its own mutex and durable journal lock while it
// waits. Release the cross-provider gate first so another provider can proceed.
func finalizeResetRejectionAfterGate(releaseGate func(), finalize func()) {
	releaseGate()
	finalize()
}

// Only use after validated rejection, and after a consumer has returned when
// one ran. A lost or ambiguous response is never proof of non-submission.
func finalizeResetRejection(provider string, resetter ProviderResetter, request ResetRequest) {
	if provider != "claude" {
		return
	}
	finalizer, ok := resetter.(ProviderResetRejectionFinalizer)
	if !ok {
		return
	}
	// Wait synchronously for durable evidence even after client cancellation or
	// the response deadline. Healthy contention must not abandon this UUID.
	// A crash or failed durable write still leaves the receipt latched.
	_ = finalizer.RecordResetNotSubmitted(context.Background(), request.RequestID, request.AccountFingerprint)
}

func decodeResetRequest(decoder *json.Decoder, allowKind bool) (codexResetRequest, error) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return codexResetRequest{}, fmt.Errorf("invalid JSON")
	}
	var request codexResetRequest
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return codexResetRequest{}, fmt.Errorf("invalid JSON")
		}
		seen[key] = true
		switch key {
		case "request_id":
			err = decoder.Decode(&request.RequestID)
		case "confirmed":
			err = decoder.Decode(&request.Confirmed)
		case "account_fingerprint":
			err = decoder.Decode(&request.AccountFingerprint)
		case "kind":
			if !allowKind {
				return codexResetRequest{}, fmt.Errorf("invalid JSON")
			}
			request.kindPresent = true
			err = decoder.Decode(&request.Kind)
		default:
			return codexResetRequest{}, fmt.Errorf("invalid JSON")
		}
		if err != nil {
			return codexResetRequest{}, fmt.Errorf("invalid JSON")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) < 3 || len(seen) > 4 || len(seen) == 4 && !allowKind {
		return codexResetRequest{}, fmt.Errorf("invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return codexResetRequest{}, fmt.Errorf("invalid JSON")
	}
	if request.kindPresent && !resetKindPattern.MatchString(request.Kind) {
		return codexResetRequest{}, fmt.Errorf("invalid JSON")
	}
	return request, nil
}

func (h *handler) route(w http.ResponseWriter, r *http.Request, method string, next http.HandlerFunc) {
	if r.Method != method {
		w.Header().Set("Allow", method)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	next(w, r)
}

func (h *handler) usageCollection(w http.ResponseWriter, _ *http.Request) {
	data := entriesData(h.cache.Snapshot())
	h.attachResetAutomation(data)
	writeJSON(w, http.StatusOK, data)
}

func (h *handler) providerUsage(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.cache.Get(strings.TrimPrefix(r.URL.Path, "/api/v1/usage/"))
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	data := []usage.UsageData{entry.Data}
	h.attachResetAutomation(data)
	writeJSON(w, http.StatusOK, data[0])
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	providers := make([]providerHealth, 0, len(h.providerNames))
	status := "ok"
	for _, name := range h.providerNames {
		entry, ok := h.cache.Get(name)
		state := providerHealth{Name: name, OK: ok && entry.Data.Error == nil}
		if ok {
			state.FetchedAt = formatFetchedAt(entry.FetchedAt)
		}
		if !state.OK {
			status = "degraded"
		}
		providers = append(providers, state)
	}
	writeJSON(w, http.StatusOK, healthResponse{Status: status, Version: h.version, Providers: providers})
}

func (h *handler) cursorCredentials(w http.ResponseWriter, r *http.Request) {
	if h.cursor == nil || h.poller == nil || !h.hasProvider("cursor") {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	request, err := decodeCredentialRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.credentialMu.Lock()
	defer h.credentialMu.Unlock()
	h.writeRefetchedProvider(w, r, "cursor", func() error {
		if request.cookie != "" {
			h.cursor.SetDesktopCookie(request.cookie, request.sourceName)
			return nil
		}
		return h.cursor.SetAccessToken(request.accessToken)
	})
}

func (h *handler) grokCredentials(w http.ResponseWriter, r *http.Request) {
	if h.grok == nil || h.poller == nil || !h.hasProvider("grok") {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	request, err := decodeCredentialRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if request.cookie == "" {
		writeError(w, http.StatusBadRequest, "provide exactly one cookie credential")
		return
	}
	h.credentialMu.Lock()
	defer h.credentialMu.Unlock()
	h.writeRefetchedProvider(w, r, "grok", func() error {
		h.grok.SetCookieHeader(request.cookie)
		return nil
	})
}

func (h *handler) writeRefetchedProvider(w http.ResponseWriter, r *http.Request, providerName string, update func() error) {
	var setterErr error
	entry, ok, err := h.poller.UpdateCredentials(r.Context(), providerName, func() error {
		setterErr = update()
		return setterErr
	})
	if setterErr != nil {
		writeError(w, http.StatusBadRequest, "invalid access token")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, providerName+" refetch failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeJSON(w, http.StatusOK, credentialResponse{Provider: entry.Data.ProviderName, Refetched: !entry.FetchedAt.IsZero(), Usage: entry.Data})
}

type credentialRequest struct {
	cookie      string
	accessToken string
	sourceName  string
}

func decodeCredentialRequest(w http.ResponseWriter, r *http.Request) (credentialRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCredentialBodyBytes)
	defer r.Body.Close()
	var raw struct {
		Cookie      *string         `json:"cookie"`
		AccessToken *string         `json:"access_token"`
		SourceName  json.RawMessage `json:"source_name"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&raw); err != nil {
		return credentialRequest{}, fmt.Errorf("invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return credentialRequest{}, fmt.Errorf("invalid JSON")
	}
	if r.URL.Path == "/api/v1/providers/grok/credentials" && raw.SourceName != nil {
		return credentialRequest{}, fmt.Errorf("invalid JSON")
	}
	cookieSet := raw.Cookie != nil && strings.TrimSpace(*raw.Cookie) != ""
	accessTokenSet := raw.AccessToken != nil && strings.TrimSpace(*raw.AccessToken) != ""
	if cookieSet == accessTokenSet {
		return credentialRequest{}, fmt.Errorf("provide exactly one credential")
	}
	request := credentialRequest{}
	if raw.SourceName != nil {
		var sourceName string
		if err := json.Unmarshal(raw.SourceName, &sourceName); err != nil {
			return credentialRequest{}, fmt.Errorf("invalid source_name")
		}
		name := strings.TrimSpace(sourceName)
		if !cookieSet || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 40 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return credentialRequest{}, fmt.Errorf("invalid source_name")
		}
		request.sourceName = name
	}
	if cookieSet {
		request.cookie = strings.TrimSpace(*raw.Cookie)
	} else {
		request.accessToken = strings.TrimSpace(*raw.AccessToken)
	}
	return request, nil
}

// attachResetAutomation adds the server-owned state when a response is written.
// It never enters the poller cache.
func (h *handler) attachResetAutomation(data []usage.UsageData) {
	if h.automation == nil {
		return
	}
	for index := range data {
		provider := resetAutomationProvider(data[index])
		if provider == "" {
			continue
		}
		if state, ok := h.automation.State(provider); ok {
			data[index].ResetAutomation = state
		}
	}
}

func entriesData(entries []poller.Entry) []usage.UsageData {
	out := make([]usage.UsageData, len(entries))
	for i, entry := range entries {
		out[i] = entry.Data
	}
	return out
}

func (h *handler) hasProvider(name string) bool {
	for _, providerName := range h.providerNames {
		if strings.EqualFold(providerName, name) {
			return true
		}
	}
	return false
}
