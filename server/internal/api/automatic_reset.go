package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/resetauto"
	"github.com/AaronFeledy/claude-usage-widget/server/internal/usage"
)

const maxAutomaticResetKinds = 8

var errInvalidAutomaticReset = errors.New("invalid automatic reset request")

type automaticResetRequest struct {
	Confirmed          bool
	AccountFingerprint string
	WindowEndsAt       time.Time
	Kinds              []string
	kindsPresent       bool
}

// decodeAutomaticResetRequest rejects unknown and duplicate keys and any
// trailing data. kinds is required for Claude and forbidden for Codex.
func decodeAutomaticResetRequest(decoder *json.Decoder, allowKinds bool) (automaticResetRequest, error) {
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return automaticResetRequest{}, errInvalidAutomaticReset
	}
	var request automaticResetRequest
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || seen[key] {
			return automaticResetRequest{}, errInvalidAutomaticReset
		}
		seen[key] = true
		switch key {
		case "confirmed":
			err = decoder.Decode(&request.Confirmed)
		case "account_fingerprint":
			err = decoder.Decode(&request.AccountFingerprint)
		case "window_ends_at":
			var value string
			if err = decoder.Decode(&value); err == nil {
				parsed, parseErr := time.Parse(time.RFC3339, value)
				if parseErr != nil {
					err = errInvalidAutomaticReset
				} else {
					request.WindowEndsAt = parsed.UTC()
				}
			}
		case "kinds":
			if !allowKinds {
				return automaticResetRequest{}, errInvalidAutomaticReset
			}
			request.kindsPresent = true
			err = decoder.Decode(&request.Kinds)
		default:
			return automaticResetRequest{}, errInvalidAutomaticReset
		}
		if err != nil {
			return automaticResetRequest{}, errInvalidAutomaticReset
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return automaticResetRequest{}, errInvalidAutomaticReset
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return automaticResetRequest{}, errInvalidAutomaticReset
	}
	required := 3
	if allowKinds {
		required = 4
	}
	if len(seen) != required || !request.Confirmed || !accountFingerprintPattern.MatchString(request.AccountFingerprint) {
		return automaticResetRequest{}, errInvalidAutomaticReset
	}
	if !allowKinds {
		if request.kindsPresent {
			return automaticResetRequest{}, errInvalidAutomaticReset
		}
		return request, nil
	}
	if !request.kindsPresent || len(request.Kinds) < 1 || len(request.Kinds) > maxAutomaticResetKinds {
		return automaticResetRequest{}, errInvalidAutomaticReset
	}
	unique := make(map[string]bool, len(request.Kinds))
	for _, id := range request.Kinds {
		if !resetKindPattern.MatchString(id) || unique[id] {
			return automaticResetRequest{}, errInvalidAutomaticReset
		}
		unique[id] = true
	}
	return request, nil
}

// automaticResetRoute serves the same transport guards as the manual reset:
// bearer auth from the chain, browser-origin rejection, JSON for PUT.
func (h *handler) automaticResetRoute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodPut+", "+http.MethodDelete)
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	provider := automaticResetProvider(r.URL.Path)
	if provider == "" || h.cache == nil || h.poller == nil || !h.hasProvider(provider) {
		writeError(w, http.StatusNotFound, "provider not found")
		return
	}
	if len(r.Header.Values("Origin")) > 0 || len(r.Header.Values("Sec-Fetch-Site")) > 0 {
		writeError(w, http.StatusForbidden, "browser requests are not allowed")
		return
	}
	if h.automation == nil {
		writeError(w, http.StatusServiceUnavailable, "automatic reset state unavailable")
		return
	}
	if r.Method == http.MethodDelete {
		state, err := h.automation.Cancel(r.Context(), provider)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "automatic reset state unavailable")
			return
		}
		writeJSON(w, http.StatusOK, state)
		return
	}
	h.armAutomaticReset(w, r, provider)
}

func (h *handler) armAutomaticReset(w http.ResponseWriter, r *http.Request, provider string) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxResetBodyBytes)
	defer r.Body.Close()
	request, err := decodeAutomaticResetRequest(json.NewDecoder(r.Body), provider == resetauto.ProviderClaude)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	entry, _ := h.cache.Get(provider)
	state, err := h.automation.Arm(r.Context(), provider, resetauto.ArmRequest{
		AccountFingerprint: request.AccountFingerprint,
		WindowEndsAt:       request.WindowEndsAt,
		Kinds:              request.Kinds,
	}, entry)
	if err != nil {
		var notEligible resetauto.NotEligibleError
		switch {
		case errors.As(err, &notEligible):
			writeError(w, http.StatusConflict, automaticResetIneligibleMessage(notEligible.Code))
		case errors.Is(err, resetauto.ErrConflicting):
			writeError(w, http.StatusConflict, "a different automatic reset is already armed")
		case errors.Is(err, resetauto.ErrSpendInProgress):
			writeError(w, http.StatusConflict, "an automatic reset is in progress")
		default:
			writeError(w, http.StatusServiceUnavailable, "automatic reset state unavailable")
		}
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func automaticResetIneligibleMessage(code string) string {
	switch code {
	case resetauto.ArmRejectNoReading:
		return "a successful reading with banked resets is required"
	case resetauto.ArmRejectIdentity:
		return "account_fingerprint does not match the latest reading"
	case resetauto.ArmRejectNoCredits:
		return "no banked resets are available"
	case resetauto.ArmRejectNoWeekly:
		return "the reading has no weekly window"
	case resetauto.ArmRejectWindow:
		return "window_ends_at must match the future weekly reset"
	case resetauto.ArmRejectKindsRequired:
		return "kinds is required for Claude"
	case resetauto.ArmRejectFloor:
		return "weekly usage must be at least 90%"
	case resetauto.ArmRejectKindUnavailable:
		return "a requested kind has no resets available"
	case resetauto.ArmRejectNoTrigger:
		return "no requested kind applies to a reported meter"
	case resetauto.ArmRejectNotUrgent:
		return "usage is not near a limit and no reset is usable now"
	default:
		return "automatic reset is not eligible"
	}
}

func automaticResetProvider(path string) string {
	switch path {
	case "/api/v1/providers/claude/reset/automatic":
		return resetauto.ProviderClaude
	case "/api/v1/providers/codex/reset/automatic":
		return resetauto.ProviderCodex
	default:
		return ""
	}
}

// resetAutomationProvider names the default row that may carry state. Extra
// Claude accounts and every other provider report a null key.
func resetAutomationProvider(data usage.UsageData) string {
	switch strings.ToLower(strings.TrimSpace(data.ProviderName)) {
	case resetauto.ProviderClaude:
		if data.Account == nil {
			return resetauto.ProviderClaude
		}
	case resetauto.ProviderCodex:
		return resetauto.ProviderCodex
	}
	return ""
}
