package cmd

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/urfave/cli/v2"
)

// placeholderPage is the static page a new app serves until its owner builds
// something. Embedded (not inlined) so the markup lives in its own file.
//
//go:embed placeholder.html
var placeholderPage string

var cmdPlaceholder = &cli.Command{
	Name:  "placeholder",
	Usage: "Serve hostit's built-in placeholder app (a new app runs this until it is built)",
	Flags: []cli.Flag{
		&cli.IntFlag{Name: "port", Aliases: []string{"p"}, Usage: "port to listen on; defaults to $PORT"},
	},
	Action: execPlaceholder,
}

// placeholderHandler serves the static placeholder page at "/" and 404s the rest.
// It is a real running server (a new app's default run: command) so the app is
// reachable the moment it is created, but the page itself is static.
func placeholderHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, placeholderPage)
	})
	return mux
}

func execPlaceholder(c *cli.Context) error {
	port := c.Int("port")
	if port == 0 {
		port, _ = strconv.Atoi(os.Getenv("PORT"))
	}
	if port == 0 {
		return errors.New("no port: pass --port or set $PORT")
	}
	addr := fmt.Sprintf("0.0.0.0:%d", port)
	fmt.Printf("Placeholder app serving on %s\n", addr)
	server := &http.Server{
		Addr:              addr,
		Handler:           placeholderHandler(),
		ReadHeaderTimeout: staticReadHeaderTimeout,
	}
	return server.ListenAndServe()
}
