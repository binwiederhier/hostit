package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

// errorPageTemplate renders the pages visitors see when an app subdomain does
// not serve anything. It deliberately says little: the headline is for casual
// visitors, and only the app-down page carries an owner hint (which reveals
// nothing a visitor could not guess from the URL).
var errorPageTemplate = template.Must(template.New("errorpage").Parse(`<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  :root { color-scheme: light dark; --fg: #14181d; --muted: #5b6672; --bg: #f6f7f9; --card: #fff; --line: #e3e7ec; --dot: #f59e0b; }
  @media (prefers-color-scheme: dark) {
    :root { --fg: #e8ecf1; --muted: #97a3b0; --bg: #14181d; --card: #1b2027; --line: #2a313a; }
  }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px;
         background: var(--bg); color: var(--fg);
         font: 16px/1.6 ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif; }
  .card { width: 100%; max-width: 520px; background: var(--card); border: 1px solid var(--line);
          border-radius: 14px; padding: 32px; }
  h1 { margin: 0; font-size: 21px; letter-spacing: -0.01em; }
  h1 .dot { display: inline-block; width: 9px; height: 9px; border-radius: 50%;
            background: var(--dot); margin-right: 9px; vertical-align: middle; }
  p { margin: 12px 0 0; color: var(--muted); }
  details { margin-top: 22px; padding-top: 16px; border-top: 1px solid var(--line); }
  summary { cursor: pointer; font-size: 14px; color: var(--muted); }
  summary::marker { color: var(--line); }
  pre { margin: 14px 0 0; padding: 14px 16px; background: var(--bg); border: 1px solid var(--line);
        border-radius: 10px; overflow-x: auto;
        font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 13.5px; }
  .foot { margin-top: 22px; padding-top: 16px; border-top: 1px solid var(--line); font-size: 13px; color: var(--muted); }
</style>
<div class="card">
  <h1><span class="dot"></span>{{.Headline}}</h1>
  <p>{{.Message}}</p>
  {{if .OwnerHint}}
  <details>
    <summary>Is this your app?</summary>
    <pre>{{.OwnerHint}}</pre>
  </details>
  {{end}}
  <div class="foot">hostit</div>
</div>
`))

// errorPageData is the template input
type errorPageData struct {
	Title     string
	Headline  string
	Message   string
	OwnerHint string
}

// writeAppDownPage is served when an app exists but nothing answers on its port
func (s *Server) writeAppDownPage(w http.ResponseWriter, appName string) {
	hint := fmt.Sprintf("ssh %s@%s\nhostit status     # is it running?\nhostit logs -n 50 # why did it stop?\nhostit up         # start it again",
		appName, s.config.SSHHostname())
	s.writeErrorPage(w, http.StatusBadGateway, &errorPageData{
		Title:     appName + " is not running",
		Headline:  "This app is not running",
		Message:   "It exists, but nothing is answering right now. If you were expecting content here, check back in a moment.",
		OwnerHint: hint,
	})
}

// writeUnknownAppPage is served for hostnames that belong to no app; it must not
// reveal whether a name is taken
func (s *Server) writeUnknownAppPage(w http.ResponseWriter) {
	s.writeErrorPage(w, http.StatusNotFound, &errorPageData{
		Title:    "Nothing here",
		Headline: "There is nothing here",
		Message:  "No app is published at this address.",
	})
}

func (s *Server) writeErrorPage(w http.ResponseWriter, status int, data *errorPageData) {
	var b strings.Builder
	if err := errorPageTemplate.Execute(&b, data); err != nil {
		http.Error(w, data.Headline, status) // Fall back to plain text
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(b.String()))
}
