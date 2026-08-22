package control

import (
	"net/http"

	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/proxyapi"
)

const (
	// webCSPBase locks the web app to its own origin. Inline styles are allowed
	// because React sets style attributes; scripts and everything else must come
	// from us, and frame-ancestors 'none' stops the dashboard being framed and its
	// privileged actions clickjacked. frame-src is appended per-server so the
	// dashboard can preview the owner's own apps (see webCSP).
	webCSPBase = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
)

// webCSP is webCSPBase plus a frame-src that allows previewing the owner's own
// apps (subdomains of the base domain) inside the dashboard, and nothing else.
func (s *Server) webCSP() string {
	return webCSPBase + "; frame-src 'self' https://*." + s.config.BaseDomain
}

// withBaseSecurityHeaders sets the headers safe for every public response,
// tenant apps included: nosniff, a conservative Referrer-Policy, and (under TLS)
// HSTS across every subdomain.
func (s *Server) withBaseSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", proxyapi.ContentTypeOptions)
		h.Set("Referrer-Policy", proxyapi.ReferrerPolicy)
		if s.config.TLS != controlconf.TLSOff {
			h.Set("Strict-Transport-Security", proxyapi.HSTSValue)
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
		h.Set("Content-Security-Policy", s.webCSP())
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
