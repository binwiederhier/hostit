package appctl

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// StaticHandler serves a directory of files: exactly what a plain HTML app
// needs, without the app having to bring a web server of its own. Directory
// listings are off (a folder without an index is a 404), so nothing is exposed
// that the author did not put there on purpose.
func StaticHandler(dir string) http.Handler {
	root := http.Dir(dir)
	fileServer := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(filepath.Clean("/"+strings.TrimPrefix(r.URL.Path, "/")), "/")
		if name != "" {
			f, err := root.Open("/" + name)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			stat, err := f.Stat()
			_ = f.Close()
			if err != nil {
				http.NotFound(w, r)
				return
			}
			// A directory only resolves when it has an index; never list it
			if stat.IsDir() {
				if _, err := os.Stat(filepath.Join(dir, name, "index.html")); err != nil {
					http.NotFound(w, r)
					return
				}
			}
		}
		fileServer.ServeHTTP(w, r)
	})
}
