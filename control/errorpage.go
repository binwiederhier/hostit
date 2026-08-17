package control

import (
	_ "embed"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"heckel.io/hostit/controlconf"
)

// errorPageHTML is the "nothing here" page's markup (a self-contained HTML
// template), embedded rather than inlined as a raw string so the large blob lives
// in its own file.
//
//go:embed errorpage.html
var errorPageHTML string

// errorPageTemplate renders the page visitors see when an app subdomain does
// not serve anything. It deliberately says little and looks the same whether the
// name is free or a registered app that is merely stopped: anything else lets an
// outsider enumerate which app names are taken. The owner learns their app is
// down from the dashboard, not from this page.
var errorPageTemplate = template.Must(template.New("errorpage").Parse(errorPageHTML))

// errorPageData is the template input. Title/Headline/Message come from the
// caller; Code and Home are filled in by writeErrorPage.
type errorPageData struct {
	Title    string
	Headline string
	Message  string
	Code     string // HTTP status, shown as the "Error 404" eyebrow
	Home     string // the dashboard URL, for the footer logo/link
}

// writeNothingHerePage is served both for a hostname that belongs to no app and
// for an app that exists but is not answering. The two cases are deliberately
// identical, so a visitor cannot tell a free name from a stopped app.
func (s *Server) writeNothingHerePage(w http.ResponseWriter) {
	s.writeErrorPage(w, http.StatusNotFound, &errorPageData{
		Title:    "404 - nothing deployed here",
		Headline: "Nothing's deployed here",
		Message:  "No app answers at this address. It was never built, or it wandered off. Either way, the spot is yours for the taking.",
	})
}

func (s *Server) writeErrorPage(w http.ResponseWriter, status int, data *errorPageData) {
	data.Code = strconv.Itoa(status)
	scheme := "https"
	if s.config.TLS == controlconf.TLSOff {
		scheme = "http"
	}
	data.Home = scheme + "://" + s.config.APIHostname()
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
