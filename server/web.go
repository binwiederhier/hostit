package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// site holds the built React app (see web/ and "make web"). The directory is
// committed with a placeholder so the package always builds; a real build
// overwrites it.
//
//go:embed site
var site embed.FS

const (
	// indexFile is served for all non-asset paths, so client-side routing works
	indexFile = "index.html"
	// assetDir is where Vite writes content-hashed assets (see web/vite.config.js
	// assetsDir); everything under it is safe to cache forever
	assetDir = "static/media/"
	// assetCacheControl is used for hashed Vite assets, which never change
	assetCacheControl = "public, max-age=31536000, immutable"
)

// webHandler serves the embedded single-page app: real files when they exist,
// index.html otherwise (SPA fallback), so /profile and /admin work on reload
func (s *Server) webHandler() http.Handler {
	sub, err := fs.Sub(site, "site")
	if err != nil {
		panic(err) // Only possible if the embedded directory is missing at build time
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/")
		if name == "" {
			s.serveIndex(w, r, sub)
			return
		}
		f, err := sub.Open(name)
		if err != nil {
			s.serveIndex(w, r, sub) // Unknown path: let the SPA router handle it
			return
		}
		defer f.Close()
		if stat, err := f.Stat(); err == nil && stat.IsDir() {
			s.serveIndex(w, r, sub)
			return
		}
		if strings.HasPrefix(name, assetDir) {
			w.Header().Set("Cache-Control", assetCacheControl)
		}
		fileServer.ServeHTTP(w, r)
	})
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	b, err := fs.ReadFile(sub, indexFile)
	if err != nil {
		http.Error(w, "404 page not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(b)
}
