package api

import (
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/AaronFeledy/claude-usage-widget/server/internal/tlsidentity"
)

func (h *handler) tlsProof(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || h.authToken == "" || h.tlsFingerprint == "" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	nonce := r.URL.Query().Get("nonce")
	if decoded, err := hex.DecodeString(nonce); err != nil || len(decoded) != 32 {
		writeError(w, http.StatusBadRequest, "invalid nonce")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, struct {
		CertificateSHA256 string `json:"certificate_sha256"`
		Proof             string `json:"proof"`
	}{h.tlsFingerprint, tlsidentity.Proof(h.authToken, strings.ToLower(nonce), h.tlsFingerprint)})
}
