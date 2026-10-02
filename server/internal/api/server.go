package api

import (
	"net/http"
	"slices"
	"strings"
)

func NewHandler(opts Options) http.Handler {
	version := opts.Version
	if version == "" {
		version = DefaultVersion
	}
	h := &handler{cache: opts.Cache, cursor: opts.Cursor, grok: opts.Grok, codex: opts.Codex, poller: opts.Poller, version: version, providerNames: normalizedNames(opts.ProviderNames)}
	h.authToken, h.tlsFingerprint = opts.AuthToken, opts.TLSCertificateSHA256
	return chain(opts, h)
}

func normalizedNames(names []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(names))
	for _, name := range names {
		trimmed := strings.TrimSpace(name)
		key := strings.ToLower(trimmed)
		if trimmed != "" && !seen[key] {
			seen[key] = true
			out = append(out, trimmed)
		}
	}
	slices.SortFunc(out, func(a string, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return out
}
