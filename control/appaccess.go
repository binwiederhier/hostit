package control

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"heckel.io/hostit/controlconf"
	"heckel.io/hostit/store"
)

// Reaching a PRIVATE app is a two-hostname problem. The session cookie is
// __Host- prefixed (see session.go), so the browser sends it only to the exact
// host that set it -- the web app. A request to blog.apps.example.com carries
// no session at all, and no amount of asking control "who is this?" can change
// that. The prefix is deliberate and worth keeping: it is what stops an app
// subdomain from planting a session on the web app.
//
// So the visitor acquires a second, much smaller credential that IS valid on
// the app's hostname. One redirect through the web app, where the session does
// apply, mints a signed grant naming the app and the user; the app hostname
// turns it into a cookie and serves from then on.
//
// Two properties make that cookie safe to leave on a hostname whose content
// the app's owner controls. It names ONE app, so it is worthless anywhere
// else, and it is stripped from the request before it is forwarded upstream,
// so the app never sees it at all. It asserts identity only -- every request
// re-checks the grant against the live owner/collaborator state, so revoking
// somebody takes effect on their next request rather than when a cache expires.

const (
	// appGrantCookieName holds the per-app grant on the app's own hostname.
	appGrantCookieName = "hostit_app"
	// appGrantTTL is how long a visitor goes before bouncing through the web app
	// again. It bounds staleness of IDENTITY only, not of access.
	appGrantTTL = 12 * time.Hour
	// appAccessPath mints a grant. It lives on the WEB hostname, which is the
	// whole point: that is where the session cookie applies.
	appAccessPath = "/auth/app"
	// appGrantPath receives a minted grant on the APP's hostname and turns it
	// into a cookie.
	appGrantPath = "/__hostit/grant"
	// grantedParam marks the one hop back from appGrantPath, so a browser that
	// refuses the cookie gets a 404 instead of an endless bounce.
	grantedParam = "hostit_granted"
	appParam     = "app"
	returnParam  = "to"
	// nextParam tells the login where to send the visitor afterwards.
	nextParam  = "next"
	grantParam = "g"
	// grantKeyLabel derives the grant signing key from the session key, so a
	// session cookie can never be replayed as a grant, nor a grant as a session.
	grantKeyLabel = "hostit-app-grant"
)

var (
	errInvalidGrant = errors.New("invalid or expired app grant")
)

// grantManager signs and verifies per-app grants. Stateless like sessions: the
// value is "<app>|<userID>|<expiry>|<hmac>".
type grantManager struct {
	key []byte
	ttl time.Duration
}

func newGrantManager(sessionKey string) *grantManager {
	mac := hmac.New(sha256.New, []byte(sessionKey))
	mac.Write([]byte(grantKeyLabel))
	return &grantManager{key: mac.Sum(nil), ttl: appGrantTTL}
}

// encode returns a signed grant naming one app and one user
func (g *grantManager) encode(app, userID string) (string, error) {
	if strings.Contains(app, "|") || strings.Contains(userID, "|") {
		return "", fmt.Errorf("invalid app %q or user id %q", app, userID)
	}
	payload := fmt.Sprintf("%s|%s|%d", app, userID, time.Now().Add(g.ttl).Unix())
	return payload + "|" + g.sign(payload), nil
}

// decode verifies a grant and returns the app and user it names
func (g *grantManager) decode(value string) (string, string, error) {
	parts := strings.Split(value, "|")
	if len(parts) != 4 {
		return "", "", errInvalidGrant
	}
	payload, signature := strings.Join(parts[:3], "|"), parts[3]
	if !hmac.Equal([]byte(signature), []byte(g.sign(payload))) {
		return "", "", errInvalidGrant
	}
	expiry, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return "", "", errInvalidGrant
	}
	if time.Now().After(time.Unix(expiry, 0)) {
		return "", "", errInvalidGrant
	}
	return parts[0], parts[1], nil
}

func (g *grantManager) sign(payload string) string {
	mac := hmac.New(sha256.New, g.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// mayViewApp reports whether the caller may reach the app over HTTP. It is the
// one visibility decision: the app proxy, the grant mint and the preview
// screenshotter all ask it, so there is no second place to get it wrong.
func (s *Server) mayViewApp(c *caller, a *store.App) bool {
	if c == nil || a == nil {
		return false
	}
	if c.globalAdmin {
		return true
	}
	// An app-scoped token exists to be pasted into ONE app's agent, and speaks
	// for that app whether or not it has an owner attached.
	if c.appScope != "" {
		return c.appScope == a.Name
	}
	if c.user == nil || !c.isActive() {
		return false
	}
	return c.isAdmin() || a.OwnerID == c.user.ID || s.apps.Store().IsAppCollaborator(a.ID, c.user.ID)
}

// allowPrivateRequest decides whether a request for a private app may proceed.
// It reports false having ALREADY answered the request -- with the grant
// redirect, the grant callback, or the same "nothing here" page an unknown
// hostname gets. That page is the refusal on purpose: a private app that
// announced itself with a 403 would be the one case where guessing a hostname
// tells you something (see errorpage.go).
func (s *Server) allowPrivateRequest(w http.ResponseWriter, r *http.Request, a *store.App) bool {
	// The hop back from the web app, carrying a freshly minted grant.
	if r.URL.Path == appGrantPath {
		s.handleAppGrant(w, r, a)
		return false
	}
	// A token-authenticated caller needs no browser dance: webhooks, scripts and
	// the CLI present a bearer token and are judged on the spot.
	if r.Header.Get("Authorization") != "" {
		c, err := s.authenticate(r)
		if err != nil || !s.mayViewApp(c, a) {
			s.writeNothingHerePage(w)
			return false
		}
		return true
	}
	// A grant cookie asserts WHO the visitor is; whether they may still see this
	// app is re-decided here, every time, against live grants.
	if c, ok := s.callerFromGrant(r, a); ok && s.mayViewApp(c, a) {
		return true
	}
	// A page load gets one bounce through the web app to pick up a grant.
	// Anything else -- an XHR, a POST, or a return trip that already failed to
	// keep the cookie -- fails closed rather than looping.
	if isNavigation(r) && !r.URL.Query().Has(grantedParam) {
		http.Redirect(w, r, s.appAccessURL(a.Name, r), http.StatusFound)
		return false
	}
	s.writeNothingHerePage(w)
	return false
}

// callerFromGrant resolves the visitor named by a valid grant cookie for this
// app. It returns only identity; authorization is mayViewApp's job.
func (s *Server) callerFromGrant(r *http.Request, a *store.App) (*caller, bool) {
	cookie, err := r.Cookie(s.cookieName(appGrantCookieName))
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	app, userID, err := s.grants.decode(cookie.Value)
	if err != nil || app != a.Name {
		return nil, false
	}
	u, err := s.users.User(userID)
	if err != nil {
		return nil, false
	}
	return &caller{user: u, viaCookie: true}, true
}

// handleAppGrant runs on the APP's hostname: it verifies a grant minted by the
// web app, stores it as a cookie scoped to this host, and sends the visitor on
// to what they originally asked for.
func (s *Server) handleAppGrant(w http.ResponseWriter, r *http.Request, a *store.App) {
	app, _, err := s.grants.decode(r.URL.Query().Get(grantParam))
	if err != nil || app != a.Name {
		s.writeNothingHerePage(w)
		return
	}
	http.SetCookie(w, s.cookie(s.cookieName(appGrantCookieName), r.URL.Query().Get(grantParam), int(appGrantTTL.Seconds())))
	http.Redirect(w, r, withParam(localPath(r.URL.Query().Get(returnParam)), grantedParam, "1"), http.StatusFound)
}

// handleAppAccess runs on the WEB hostname, where the session cookie applies:
// it decides whether the signed-in visitor may see the app and, if so, sends
// them back to it with a grant. Everything else gets the "nothing here" page,
// so a stranger poking at hostnames learns nothing an unknown name would not
// also tell them -- including whether they simply need to sign in.
func (s *Server) handleAppAccess(w http.ResponseWriter, r *http.Request) {
	name, target := r.URL.Query().Get(appParam), r.URL.Query().Get(returnParam)
	a, err := s.apps.App(name)
	if err != nil {
		s.writeNothingHerePage(w)
		return
	}
	c, err := s.authenticate(r)
	if err != nil {
		// Nobody is signed in. Send them to sign in and come back, rather than
		// to a page they cannot act on: an owner opening their own app from a
		// browser they have not used before is the common case here, and a dead
		// end there reads as the app being broken. This does tell an anonymous
		// visitor that the name exists; a signed-in stranger is still refused
		// below, so one hostit user cannot enumerate another's private apps.
		if !s.config.WebEnabled() {
			s.writeNothingHerePage(w)
			return
		}
		here := (&url.URL{Path: appAccessPath, RawQuery: r.URL.RawQuery}).String()
		http.Redirect(w, r, (&url.URL{Path: "/auth/google", RawQuery: url.Values{nextParam: {here}}.Encode()}).String(), http.StatusFound)
		return
	}
	if !s.mayViewApp(c, a) {
		s.writeNothingHerePage(w)
		return
	}
	// The return URL decides where the grant is DELIVERED, so it has to be one
	// of this app's own hostnames. Without the check it is both an open
	// redirect and a way to have a grant posted to a host of one's choosing.
	back, err := s.appReturnURL(a, target)
	if err != nil {
		s.writeNothingHerePage(w)
		return
	}
	grant, err := s.grants.encode(a.Name, c.userID())
	if err != nil {
		s.writeNothingHerePage(w)
		return
	}
	query := url.Values{grantParam: {grant}, returnParam: {back.RequestURI()}}
	http.Redirect(w, r, (&url.URL{Scheme: back.Scheme, Host: back.Host, Path: appGrantPath, RawQuery: query.Encode()}).String(), http.StatusFound)
}

// appReturnURL validates that a return URL really is one of the app's own
// hostnames -- its <app>.<base-domain> name or a custom domain that currently
// resolves to it.
func (s *Server) appReturnURL(a *store.App, target string) (*url.URL, error) {
	u, err := url.Parse(target)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, errors.New("not an absolute return URL")
	}
	host := hostOnly(u.Host)
	if host == a.Name+"."+s.config.BaseDomain {
		return u, nil
	}
	if name, ok := s.appNameFromCustomDomain(host); ok && name == a.Name {
		return u, nil
	}
	return nil, fmt.Errorf("%q is not a hostname of app %q", host, a.Name)
}

// appAccessURL is where a visitor is sent to pick up a grant: the web app, with
// the URL they were trying to reach in hand.
func (s *Server) appAccessURL(name string, r *http.Request) string {
	query := url.Values{appParam: {name}, returnParam: {s.requestURL(r).String()}}
	return (&url.URL{Scheme: s.scheme(), Host: s.config.APIHostname(), Path: appAccessPath, RawQuery: query.Encode()}).String()
}

// requestURL reconstructs the absolute URL a visitor asked for. The request
// itself carries only a path, and the Host header names the app.
func (s *Server) requestURL(r *http.Request) *url.URL {
	return &url.URL{Scheme: s.scheme(), Host: r.Host, Path: r.URL.Path, RawQuery: r.URL.RawQuery}
}

func (s *Server) scheme() string {
	if s.config.TLS == controlconf.TLSOff {
		return "http"
	}
	return "https"
}

// stripGrantCookie removes the grant from the Cookie header before the request
// goes upstream, so an app never sees the credential that let its visitor in --
// even though the cookie is on its own hostname.
func stripGrantCookie(r *http.Request, name string) {
	cookies := r.Cookies()
	kept := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if c.Name != name {
			kept = append(kept, c.String())
		}
	}
	if len(kept) == len(cookies) {
		return
	}
	r.Header.Del("Cookie")
	if len(kept) > 0 {
		r.Header.Set("Cookie", strings.Join(kept, "; "))
	}
}

// isNavigation reports whether a request is a browser loading a page, as
// opposed to a sub-resource or an API call. Only a navigation is worth
// redirecting: bouncing an XHR through the web app would hand the app's
// JavaScript an HTML page where it expected data.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" {
		return mode == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// localPath keeps a return target on this host: a path, never a URL pointing
// somewhere else, and never a protocol-relative "//host" that a browser reads
// as an absolute URL.
func localPath(target string) string {
	if !strings.HasPrefix(target, "/") || strings.HasPrefix(target, "//") {
		return "/"
	}
	return target
}

// withParam appends a query parameter to a path that may already have some.
func withParam(target, key, value string) string {
	u, err := url.Parse(target)
	if err != nil {
		return target
	}
	query := u.Query()
	query.Set(key, value)
	u.RawQuery = query.Encode()
	return u.String()
}
