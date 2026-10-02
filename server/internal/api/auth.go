package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

func authMiddleware(token string, next http.Handler) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/tls/proof" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		prefix := "Bearer "
		if !strings.HasPrefix(auth, prefix) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		presented := strings.TrimPrefix(auth, prefix)
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(token)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}
