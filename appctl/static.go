package appctl

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	// indexFile is what makes a directory servable; without it, a directory is
	// a 404 rather than a listing
	indexFile = "index.html"
)

// StaticHandler serves a directory of files: exactly what a plain HTML app
// needs, without the app having to bring a web server of its own.
//
// Two things it will not do, whatever directory it is pointed at. It never
// lists a directory -- a folder without an index.html is a 404, the root
// included -- and it never serves a path with a hidden segment. Both guard the
// same failure: this handler publishes to the open internet, so it must expose
// only what the author put there on purpose.
func StaticHandler(dir string) http.Handler {
	root := http.Dir(dir)
	fileServer := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(filepath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")), "/")
		if hasHiddenSegment(name) {
			http.NotFound(w, r)
			return
		}
		// The root is a directory too: it is the one people forget, and the one
		// that publishes an entire app when it has no index
		target := name
		if target == "" {
			target = "."
		}
		stat, err := os.Stat(filepath.Join(dir, target))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if stat.IsDir() {
			if _, err := os.Stat(filepath.Join(dir, target, indexFile)); err != nil {
				http.NotFound(w, r)
				return
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}

// hasHiddenSegment reports whether any part of a cleaned path starts with a dot
func hasHiddenSegment(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		if strings.HasPrefix(segment, ".") && segment != "." {
			return true
		}
	}
	return false
}
