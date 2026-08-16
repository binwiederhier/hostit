package server

import (
	"heckel.io/hostit/store"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
)

// proxyHandler returns the public-facing handler: admin API on the API hostname,
// reverse proxy to app ports for <app>.<base-domain>, 404 for everything else
func (s *Server) proxyHandler() http.Handler {
	return s.proxy
}

func (s *Server) newProxyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostOnly(r.Host)
		if s.config.IsWebHostname(host) {
			s.api.ServeHTTP(w, r)
			return
		}
		name, ok := s.appNameFromHost(host)
		if !ok {
			// Not an <app>.<base> subdomain; maybe a custom domain the owner attached.
			name, ok = s.appNameFromCustomDomain(host)
		}
		if !ok {
			s.writeNothingHerePage(w)
			return
		}
		a, err := s.apps.App(name)
		if err != nil {
			s.writeNothingHerePage(w)
			return
		}
		s.proxyTo(w, r, a)
	})
}

// previewParam is the query key the web app appends to a live-preview iframe URL
// (and thus carries in the Referer of that iframe's same-origin sub-resources), so
// the proxy can tell a preview load from ordinary traffic.
const previewParam = "hostit_preview"

// proxyTo forwards the request to the app's port on its hosting node (the
// loopback for local apps), preserving the original Host header and streaming
// immediately (SSE/websocket friendly)
func (s *Server) proxyTo(w http.ResponseWriter, r *http.Request, a *store.App) {
	port := a.Port
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort(s.nodeAddress(a.Host), strconv.Itoa(port))}
	// The owner's live preview must always show the latest deploy, so on a preview
	// load defeat all caching of the app's HTML and assets. Real visitors are
	// unaffected -- this only touches requests tagged as a preview.
	preview := isPreviewRequest(r)
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if preview {
				stripCachingForPreview(resp.Header)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("Proxy error", "host", r.Host, "port", port, "error", err)
			s.writeNothingHerePage(w)
		},
	}
	proxy.ServeHTTP(w, r)
}

// isPreviewRequest reports whether r is loaded inside the owner's live preview: the
// iframe tags its own URL with the preview query param, and the browser carries
// that URL in the Referer of every same-origin sub-resource (CSS/JS/images) the
// page loads. So the top-level document is matched by its query, and its assets by
// their Referer.
func isPreviewRequest(r *http.Request) bool {
	return r.URL.Query().Has(previewParam) || strings.Contains(r.Header.Get("Referer"), previewParam+"=")
}

// stripCachingForPreview rewrites response headers so a preview never serves a stale
// document or asset: no storing, and no validators the app could answer with a 304.
func stripCachingForPreview(h http.Header) {
	h.Set("Cache-Control", "no-store, must-revalidate")
	h.Del("ETag")
	h.Del("Last-Modified")
	h.Del("Expires")
}

// appNameFromHost extracts the app name from a hostname directly below the base
// domain, e.g. "blog.apps.example.com" -> "blog"
func (s *Server) appNameFromHost(host string) (string, bool) {
	suffix := "." + s.config.BaseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	name := strings.TrimSuffix(host, suffix)
	if name == "" || strings.Contains(name, ".") {
		return "", false
	}
	return name, true
}

// hostOnly strips an optional port from a Host header value
func hostOnly(hostport string) string {
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		return host
	}
	return hostport
}
