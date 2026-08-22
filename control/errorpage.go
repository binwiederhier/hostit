package control

import (
	_ "embed"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/store"
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
var (
	errorPageTemplate = template.Must(template.New("errorpage").Parse(errorPageHTML))
)

// errorPageData is the template input. Title/Headline/Message come from the
// caller; Code and Home are filled in by writeErrorPage.
type errorPageData struct {
	Title    string
	Headline string
	Message  string
	// Detail is an optional second line, for something the reader can act on.
	Detail string
	// Badge overrides the "Error 404" eyebrow, and Calm tones it down from red:
	// being refused an app you are not on the list for is a state, not a fault.
	Badge string
	Calm  bool
	Code  string // HTTP status, shown as the eyebrow when Badge is empty
	Home  string // the dashboard URL, for the footer logo/link
}

// writeNothingHerePage is served for a hostname that belongs to no app and for
// an app that exists but is not answering. The two are deliberately identical,
// so a visitor cannot tell a free name from a stopped one. Being refused a
// private app is a different answer (writePrivateAppPage): there the app's
// existence is already known to whoever was sent the link.
func (s *Server) writeNothingHerePage(w http.ResponseWriter) {
	s.writeErrorPage(w, http.StatusNotFound, &errorPageData{
		Title:    "404 - nothing deployed here",
		Headline: "Nothing's deployed here",
		Message:  "No app answers at this address. It was never built, or it wandered off. Either way, the spot is yours for the taking.",
	})
}

// writePrivateAppPage tells a signed-in visitor that the app is real, that it
// is private, and what to do about it. It deliberately says more than the
// "nothing here" page: the person reading it was almost always sent the link by
// the owner, and telling them nothing left them with no way forward. An unknown
// hostname still gets the silent page, which is where hiding app names actually
// mattered.
func (s *Server) writePrivateAppPage(w http.ResponseWriter, r *http.Request, a *store.App) {
	data := &errorPageData{
		Title:    "Private app",
		Badge:    "Private",
		Calm:     true,
		Headline: "This is a private app",
		Message:  "Ask the owner of " + a.Name + " to give you access to it.",
	}
	if email := s.visitorEmail(r); email != "" {
		data.Detail = "You are signed in as " + email + ". If that is not the account you were given access with, sign in as the other one."
	}
	s.writeErrorPage(w, http.StatusForbidden, data)
}

// visitorEmail is who the refused visitor appears to be -- but ONLY on the web
// hostname. The same page is served on the app's own origin, where the app's
// own JavaScript can fetch and read it; naming the visitor there would hand
// every tenant the identity of anyone who opened their app, admins included,
// and undo the reason the grant cookie is stripped in the first place.
func (s *Server) visitorEmail(r *http.Request) string {
	if !s.config.IsWebHostname(hostOnly(r.Host)) {
		return ""
	}
	if c, err := s.authenticate(r); err == nil && c.user != nil {
		return c.user.Email
	}
	return ""
}

func (s *Server) writeErrorPage(w http.ResponseWriter, status int, data *errorPageData) {
	data.Code = strconv.Itoa(status)
	if data.Badge == "" {
		data.Badge = "Error " + data.Code
	}
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
