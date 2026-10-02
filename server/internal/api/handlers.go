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
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const maxCredentialBodyBytes = 1 << 20
const maxResetBodyBytes = 1024
const resetOperationTimeout = 60 * time.Second
const resetWriteTimeout = 65 * time.Second

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var accountFingerprintPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type handler struct {
	authToken           string
	tlsFingerprint      string
	cache               Cache
	cursor              CursorCredentials
	grok                GrokCredentials
	codex               CodexResetter
	poller              ProviderPoller
	version             string
	providerNames       []string
	credentialMu        sync.Mutex
	resetMu             sync.Mutex
	codexRefreshMu      sync.Mutex
	codexRefreshPending bool
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
	if path == "/api/v1/providers/codex/reset" {
		h.route(w, r, http.MethodPost, h.codexReset)
		return
	}
	writeError(w, http.StatusNotFound, "not found")
}

type codexResetRequest struct {
	RequestID          string `json:"request_id"`
	Confirmed          bool   `json:"confirmed"`
	AccountFingerprint string `json:"account_fingerprint"`
}

type codexResetResponse struct {
	Outcome string `json:"outcome"`
}

// This handler can spend a very valuable banked reset. Do not test the reset
// button, this endpoint, or any code that may trigger a reset. The skipped
// safety tests are intentional and must not be converted into executable tests.
func (h *handler) codexReset(w http.ResponseWriter, r *http.Request) {
	if h.codex == nil || h.poller == nil || !h.hasProvider("codex") {
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
	decoder := json.NewDecoder(r.Body)
	request, err := decodeCodexResetRequest(decoder)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if !request.Confirmed || !uuidPattern.MatchString(request.RequestID) || !accountFingerprintPattern.MatchString(request.AccountFingerprint) {
		writeError(w, http.StatusBadRequest, "request_id must be a UUID, confirmed must be true, and account_fingerprint must be lowercase SHA-256")
		return
	}
	if resetCtx.Err() != nil {
		writeError(w, http.StatusRequestTimeout, "reset request timed out")
		return
	}

	if !h.resetMu.TryLock() {
		writeError(w, http.StatusConflict, "another reset request is in progress")
		return
	}
	defer h.resetMu.Unlock()
	known, blocked := h.codex.ResetAttemptStatus(request.RequestID)
	if blocked {
		writeError(w, http.StatusConflict, "previous reset awaiting usage confirmation; no retry permitted")
		return
	}
	expectedAccountID := ""
	if !known {
		entry, ok, err := h.poller.PollProvider(resetCtx, "codex")
		if err != nil || !ok || !eligibleForCodexReset(entry) || entry.Data.RateLimitResetCredits.AccountFingerprint == nil || *entry.Data.RateLimitResetCredits.AccountFingerprint != request.AccountFingerprint {
			writeError(w, http.StatusConflict, "fresh eligible ChatGPT usage is required")
			return
		}
		expectedAccountID = entry.Data.ProviderAccountID
	}
	outcome, ambiguous, err := h.codex.ConsumeResetCredit(resetCtx, request.RequestID, expectedAccountID, request.AccountFingerprint)
	// Reconcile even an uncertain response with a delayed read-only fetch. Never
	// repeat ConsumeResetCredit automatically or exercise this flow in tests.
	h.scheduleCodexUsageRefresh()
	if err != nil {
		if ambiguous {
			writeError(w, http.StatusBadGateway, "reset outcome unknown; request will not be retried")
		} else {
			writeError(w, http.StatusBadGateway, "reset request unavailable")
		}
		return
	}
	writeJSON(w, http.StatusOK, codexResetResponse{Outcome: outcome})
}

func (h *handler) scheduleCodexUsageRefresh() {
	h.codexRefreshMu.Lock()
	if h.codexRefreshPending {
		h.codexRefreshMu.Unlock()
		return
	}
	h.codexRefreshPending = true
	h.codexRefreshMu.Unlock()
	go func() {
		defer func() {
			h.codexRefreshMu.Lock()
			h.codexRefreshPending = false
			h.codexRefreshMu.Unlock()
		}()
		// Give OpenAI time to apply the reset, then update the ordinary usage
		// cache once. This is only a provider GET, never another redemption.
		// It outlives the response but has its own bounded fetch deadline.
		time.Sleep(3 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _, _ = h.poller.PollProvider(ctx, "codex")
	}()
}

func decodeCodexResetRequest(decoder *json.Decoder) (codexResetRequest, error) {
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
		default:
			return codexResetRequest{}, fmt.Errorf("invalid JSON")
		}
		if err != nil {
			return codexResetRequest{}, fmt.Errorf("invalid JSON")
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 3 {
		return codexResetRequest{}, fmt.Errorf("invalid JSON")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return codexResetRequest{}, fmt.Errorf("invalid JSON")
	}
	return request, nil
}

func eligibleForCodexReset(entry poller.Entry) bool {
	if entry.Data.Error != nil || strings.TrimSpace(entry.Data.ProviderAccountID) == "" || entry.Data.RateLimitResetCredits == nil || entry.Data.RateLimitResetCredits.AvailableCount <= 0 {
		return false
	}
	for _, bucket := range entry.Data.Buckets {
		if bucket.ID == usage.BucketWeekly {
			return bucket.Utilization >= 95
		}
	}
	return false
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
	writeJSON(w, http.StatusOK, entriesData(h.cache.Snapshot()))
}

func (h *handler) providerUsage(w http.ResponseWriter, r *http.Request) {
	entry, ok := h.cache.Get(strings.TrimPrefix(r.URL.Path, "/api/v1/usage/"))
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeJSON(w, http.StatusOK, entry.Data)
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
	if request.cookie != "" {
		h.cursor.SetDesktopCookie(request.cookie, request.sourceName)
	} else if err := h.cursor.SetAccessToken(request.accessToken); err != nil {
		writeError(w, http.StatusBadRequest, "invalid access token")
		return
	}
	h.writeRefetchedProvider(w, r, "cursor")
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
	h.grok.SetCookieHeader(request.cookie)
	h.writeRefetchedProvider(w, r, "grok")
}

func (h *handler) writeRefetchedProvider(w http.ResponseWriter, r *http.Request, providerName string) {
	entry, ok, err := h.poller.PollProvider(r.Context(), providerName)
	if err != nil {
		writeError(w, http.StatusBadGateway, providerName+" refetch failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	writeJSON(w, http.StatusOK, credentialResponse{Provider: entry.Data.ProviderName, Refetched: true, Usage: entry.Data})
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
