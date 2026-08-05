package server

import (
	"html/template"
	"net/http"
	"strings"
)

// errorPageTemplate renders the page visitors see when an app subdomain does
// not serve anything. It deliberately says little and looks the same whether the
// name is free or a registered app that is merely stopped: anything else lets an
// outsider enumerate which app names are taken. The owner learns their app is
// down from the dashboard, not from this page.
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
  <div class="foot">hostit</div>
</div>
`))

// errorPageData is the template input
type errorPageData struct {
	Title    string
	Headline string
	Message  string
}

// writeNothingHerePage is served both for a hostname that belongs to no app and
// for an app that exists but is not answering. The two cases are deliberately
// identical, so a visitor cannot tell a free name from a stopped app.
func (s *Server) writeNothingHerePage(w http.ResponseWriter) {
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
