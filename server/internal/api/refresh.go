package api

import (
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/poller"
)

type usageRefresher interface {
	RequestRefresh() (poller.RefreshResult, error)
}

func (h *handler) refreshUsage(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Origin")) > 0 || len(r.Header.Values("Sec-Fetch-Site")) > 0 {
		writeError(w, http.StatusForbidden, "browser requests are not allowed")
		return
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 || r.URL.RawQuery != "" || r.URL.ForceQuery {
		writeError(w, http.StatusBadRequest, "invalid refresh request")
		return
	}
	refresher, ok := h.poller.(usageRefresher)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "refresh unavailable")
		return
	}
	result, err := refresher.RequestRefresh()
	switch {
	case errors.Is(err, poller.ErrRefreshRateLimited):
		w.Header().Set("Retry-After", strconv.Itoa(result.RetryAfterSeconds))
		writeJSON(w, http.StatusTooManyRequests, struct {
			Error             string `json:"error"`
			RetryAfterSeconds int    `json:"retry_after_seconds"`
		}{"refresh rate limited", result.RetryAfterSeconds})
	case err != nil:
		writeError(w, http.StatusServiceUnavailable, "refresh unavailable")
	default:
		w.Header().Set("Retry-After", strconv.Itoa(result.RetryAfterSeconds))
		writeJSON(w, http.StatusAccepted, result)
	}
}
