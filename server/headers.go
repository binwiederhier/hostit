package server

import (
	"net/http"

	"heckel.io/hostit/config"
)

const (
	// hstsValue is two years with subdomains, the value the preload list wants.
	// One base-domain HSTS covers every app subdomain too.
	hstsValue = "max-age=63072000; includeSubDomains"
	// webCSP locks the web app to its own origin. Inline styles are allowed
	// because React sets style attributes; scripts and everything else must come
	// from us, and frame-ancestors 'none' stops the dashboard being framed and
	// its privileged actions clickjacked.
	webCSP = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
)

// withBaseSecurityHeaders sets the headers safe for every public response,
// tenant apps included: nosniff, a conservative Referrer-Policy, and (under TLS)
// HSTS across every subdomain.
func (s *Server) withBaseSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		if s.config.TLS != config.TLSOff {
			h.Set("Strict-Transport-Security", hstsValue)
		}
		next.ServeHTTP(w, r)
	})
}

// withWebSecurityHeaders adds the stricter headers for our own web app and API.
// They are deliberately kept off tenant apps, which are arbitrary content and
// may want to be framed or to set their own content rules.
func (s *Server) withWebSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", webCSP)
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
