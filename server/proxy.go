package server

import (
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
		if host == s.config.APIHostname() {
			s.api.ServeHTTP(w, r)
			return
		}
		name, ok := s.appNameFromHost(host)
		if !ok {
			http.NotFound(w, r)
			return
		}
		a, err := s.apps.App(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		s.proxyTo(w, r, a.Port)
	})
}

// proxyTo forwards the request to the app's loopback port, preserving the original
// Host header and streaming immediately (SSE/websocket friendly)
func (s *Server) proxyTo(w http.ResponseWriter, r *http.Request, port int) {
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port))}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
			pr.Out.Host = pr.In.Host
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("Proxy error", "host", r.Host, "port", port, "error", err)
			http.Error(w, "502 bad gateway: app not reachable, is it running?", http.StatusBadGateway)
		},
	}
	proxy.ServeHTTP(w, r)
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
