package middleware

import (
	"net/http"
	"strings"
)

const authPathPrefix = "/api/v1/auth/"

// SecurityHeaders sets the headers that cost nothing and close the usual
// browser-side holes. The API only returns JSON, so nothing needs to be loaded
// or framed.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")

		// Authentication responses must never be cached by a proxy or browser,
		// including the error paths that handlers do not touch.
		if strings.HasPrefix(r.URL.Path, authPathPrefix) {
			header.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

var (
	allowedCORSMethods = []string{http.MethodGet, http.MethodPost, http.MethodOptions}
	allowedCORSHeaders = []string{"Authorization", "Content-Type", "Idempotency-Key"}
)

// CORSConfig is the resolved browser policy. An empty origin list disables CORS
// entirely, which is the default because this API has no browser client.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

// CORS answers preflight requests and adds the response headers a browser needs.
func CORS(config CORSConfig) func(http.Handler) http.Handler {
	allowed := make(map[string]struct{}, len(config.AllowedOrigins))
	for _, origin := range config.AllowedOrigins {
		allowed[origin] = struct{}{}
	}

	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}

		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			// The origin is echoed rather than wildcarded, so a shared cache can
			// never hand one client's allow header to another.
			w.Header().Add("Vary", "Origin")
			if !originAllowed(allowed, origin) {
				if r.Method == http.MethodOptions {
					writeJSONError(w, http.StatusForbidden,
						"cors_origin_denied", "origin is not allowed")
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			setAllowOrigin(w, config, origin, allowed)
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				writePreflight(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(allowed map[string]struct{}, origin string) bool {
	if _, listed := allowed[origin]; listed {
		return true
	}
	_, wildcard := allowed["*"]
	return wildcard
}

// setAllowOrigin never combines a wildcard with credentials, because browsers
// reject that combination and it silently disables the CORS response.
func setAllowOrigin(
	w http.ResponseWriter,
	config CORSConfig,
	origin string,
	allowed map[string]struct{},
) {
	_, wildcard := allowed["*"]
	if wildcard && !config.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Origin", "*")
	} else {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if config.AllowCredentials {
		w.Header().Set("Access-Control-Allow-Credentials", "true")
	}
}

func writePreflight(w http.ResponseWriter, r *http.Request) {
	if !contains(allowedCORSMethods, r.Header.Get("Access-Control-Request-Method")) {
		writeJSONError(w, http.StatusForbidden, "cors_method_denied", "method is not allowed")
		return
	}
	for _, header := range requestedHeaders(r) {
		if !containsFold(allowedCORSHeaders, header) {
			writeJSONError(w, http.StatusForbidden, "cors_header_denied", "header is not allowed")
			return
		}
	}

	w.Header().Set("Access-Control-Allow-Methods", strings.Join(allowedCORSMethods, ", "))
	w.Header().Set("Access-Control-Allow-Headers", strings.Join(allowedCORSHeaders, ", "))
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func requestedHeaders(r *http.Request) []string {
	var headers []string
	for _, part := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			headers = append(headers, trimmed)
		}
	}
	return headers
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
