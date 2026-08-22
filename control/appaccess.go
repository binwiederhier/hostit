package control

import (
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
// somebody takes effect within one routing-table push rather than when a cache
// expires. (The proxy enforces from that pushed set -- see proxy/grant.go -- so
// during a control outage it keeps honouring the last set it was given, which
// is the deliberate trade for private apps staying up at all.)

const (
	// appGrantCookieName holds the per-app grant on the app's own hostname.
	appGrantCookieName = "hostit_app"
	// appGrantTTL is how long a visitor goes before bouncing through the web app
	// again. It bounds staleness of IDENTITY only, not of access.
	appGrantTTL = 12 * time.Hour
	// appAccessPath mints a grant. It lives on the WEB hostname, which is the
	// whole point: that is where the session cookie applies.
	appAccessPath = "/auth/app"
	// The three endpoints hostit answers for on a PRIVATE app's own hostname.
	// They are intercepted before the request reaches the app, so they are the
	// one place its URL space is not entirely the app's own.
	appAuthPath    = "/hostit/auth"    // start the sign-in bounce by hand
	appGrantedPath = "/hostit/granted" // take delivery of a grant, then move on
	appLogoutPath  = "/hostit/logout"  // drop the grant for this one app
	// loopCookieName catches a browser that will not keep the grant cookie. It
	// is set on the WEB host, where cookies demonstrably work (the session is
	// there), so it is a reliable place to notice that the last mint did not
	// take -- and it keeps the marker out of the app's URL. It counts rather
	// than latching, because opening the same private app in a few tabs at once
	// legitimately mints more than once.
	loopCookieName = "hostit_minting"
	loopWindow     = 15 * time.Second
	loopLimit      = 3
	appParam       = "app"
	returnParam    = "to"
	// nextParam tells the login where to send the visitor afterwards.
	nextParam  = "next"
	grantParam = "g"
)

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
	if c.isAdmin() || a.OwnerID == c.user.ID {
		return true
	}
	// Either grant is enough to look at the app. A collaborator can deploy to
	// it and SSH in, so viewing is the least of what they may already do; the
	// separate viewer grant is for people who should ONLY look.
	return s.apps.Store().IsAppCollaborator(a.ID, c.user.ID) || s.apps.Store().IsAppViewer(a.ID, c.user.ID)
}

// allowPrivateRequest decides whether a request for a private app may proceed.
// It reports false having ALREADY answered the request -- with the grant
// redirect, the grant callback, or the same "nothing here" page an unknown
// hostname gets. That page is the refusal on purpose: a private app that
// announced itself with a 403 would be the one case where guessing a hostname
// tells you something (see errorpage.go).
func (s *Server) allowPrivateRequest(w http.ResponseWriter, r *http.Request, a *store.App) bool {
	switch r.URL.Path {
	case appGrantedPath: // the hop back from the web app, carrying a grant
		s.handleAppGranted(w, r, a)
		return false
	case appLogoutPath:
		s.handleAppLogout(w, r)
		return false
	case appAuthPath: // "sign me in here", for a visitor who wants to ask
		http.Redirect(w, r, s.appAccessURL(a.Name, r), http.StatusFound)
		return false
	}
	// A token-authenticated caller needs no browser dance: webhooks, scripts and
	// the CLI present a bearer token and are judged on the spot.
	if r.Header.Get("Authorization") != "" {
		c, err := s.authenticate(r)
		if err != nil || !s.mayViewApp(c, a) {
			s.writePrivateAppPage(w, r, a)
			return false
		}
		return true
	}
	// A grant cookie asserts WHO the visitor is; whether they may still see this
	// app is re-decided here, every time, against live grants.
	if c, ok := s.callerFromGrant(r, a); ok && s.mayViewApp(c, a) {
		return true
	}
	// A page load bounces through the web app to pick up a grant. Anything else
	// -- an XHR, a POST -- fails closed rather than being handed an HTML page
	// where it expected data. (A browser that will not keep the grant is caught
	// at the mint end, so this cannot loop.)
	if isNavigation(r) {
		http.Redirect(w, r, s.appAccessURL(a.Name, r), http.StatusFound)
		return false
	}
	s.writePrivateAppPage(w, r, a)
	return false
}

// callerFromGrant resolves the visitor named by a valid grant cookie for this
// app. It returns only identity; authorization is mayViewApp's job.
func (s *Server) callerFromGrant(r *http.Request, a *store.App) (*caller, bool) {
	cookie, err := r.Cookie(s.cookieName(appGrantCookieName))
	if err != nil || cookie.Value == "" {
		return nil, false
	}
	app, userID, err := s.grants.Verifier().Verify(cookie.Value)
	if err != nil || app != a.Name {
		return nil, false
	}
	u, err := s.users.User(userID)
	if err != nil {
		return nil, false
	}
	return &caller{user: u, viaCookie: true}, true
}

// handleAppGranted runs on the APP's hostname: it verifies a grant minted by
// the web app, stores it as a cookie scoped to this host, and sends the visitor
// on to what they originally asked for -- with nothing left in the URL to show
// for it.
func (s *Server) handleAppGranted(w http.ResponseWriter, r *http.Request, a *store.App) {
	// This hop is a redirect from our own web host, which is same-site with
	// every app hostname. Anything else is somebody pushing a grant of their
	// choosing into this browser -- harmless on its own, but it is identity
	// fixation and there is no reason to accept it.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-site" && site != "same-origin" && site != "none" {
		s.writePrivateAppPage(w, r, a)
		return
	}
	app, _, err := s.grants.Verifier().Verify(r.URL.Query().Get(grantParam))
	if err != nil || app != a.Name {
		s.writePrivateAppPage(w, r, a)
		return
	}
	http.SetCookie(w, s.cookie(s.cookieName(appGrantCookieName), r.URL.Query().Get(grantParam), int(appGrantTTL.Seconds())))
	http.Redirect(w, r, localPath(r.URL.Query().Get(returnParam)), http.StatusFound)
}

// handleAppLogout drops the grant for this one app. It lands on the web app
// rather than back here, because the hostit session is untouched: returning to
// the app would silently mint a new grant and make the logout look broken.
// Signing out of hostit itself is the web app's logout.
func (s *Server) handleAppLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, s.cookie(s.cookieName(appGrantCookieName), "", -1))
	http.Redirect(w, r, (&url.URL{Scheme: s.scheme(), Host: s.config.APIHostname()}).String()+"/", http.StatusFound)
}

// handleAppAccess runs on the WEB hostname, where the session cookie applies:
// it decides whether the signed-in visitor may see the app and, if so, sends
// them back to it with a grant. Everything else gets the "nothing here" page,
// so a stranger poking at hostnames learns nothing an unknown name would not
// also tell them -- including whether they simply need to sign in.
func (s *Server) handleAppAccess(w http.ResponseWriter, r *http.Request) {
	name, target := r.URL.Query().Get(appParam), r.URL.Query().Get(returnParam)
	a, err := s.apps.App(name)
	// A public app has nothing to mint, and answering for one would turn this
	// endpoint into a way to ask whether any given app name exists.
	if err != nil || !a.Private {
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
		s.writePrivateAppPage(w, r, a)
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
	// Minting the same app over and over in seconds means the browser is not
	// keeping the grant. Stop, rather than bounce them between the two
	// hostnames until the browser gives up.
	minted := mintCount(r.Cookie(s.cookieName(loopCookieName)))[a.Name]
	if minted >= loopLimit {
		s.writePrivateAppPage(w, r, a)
		return
	}
	grant, err := s.grants.Sign(a.Name, c.userID())
	if err != nil {
		s.writeNothingHerePage(w)
		return
	}
	http.SetCookie(w, s.cookie(s.cookieName(loopCookieName), fmt.Sprintf("%s:%d", a.Name, minted+1), int(loopWindow.Seconds())))
	query := url.Values{grantParam: {grant}, returnParam: {back.RequestURI()}}
	http.Redirect(w, r, (&url.URL{Scheme: back.Scheme, Host: back.Host, Path: appGrantedPath, RawQuery: query.Encode()}).String(), http.StatusFound)
}

// appReturnURL validates that a return URL really is one of the app's own
// hostnames -- its <app>.<base-domain> name or a custom domain that currently
// resolves to it.
func (s *Server) appReturnURL(a *store.App, target string) (*url.URL, error) {
	u, err := url.Parse(target)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, errors.New("not an absolute return URL")
	}
	if u.Scheme != s.scheme() {
		return nil, fmt.Errorf("%q is not %s", target, s.scheme())
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

// localPath keeps a return target on this host: a path, and nothing a browser
// could read as pointing somewhere else.
//
// Parsing rather than prefix-matching, because the ways out are not obvious. A
// browser treats "\" as "/" in the relative-slash state, so "/\evil.example.org"
// navigates off-site; it also strips tabs and newlines before parsing, so
// "/<TAB>/evil.example.org" becomes "//evil.example.org". Both reach a
// Location header verbatim if only the leading characters are checked.
func localPath(target string) string {
	if strings.ContainsAny(target, "\\\t\r\n") {
		return "/"
	}
	u, err := url.Parse(target)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return u.RequestURI()
}

// mintCount reads the "<app>:<n>" marker the mint endpoint leaves behind. A
// malformed or absent cookie counts as zero: this is a loop brake, not a
// security control, and failing it open costs a redirect rather than access.
func mintCount(cookie *http.Cookie, err error) map[string]int {
	if err != nil || cookie == nil {
		return nil
	}
	app, count, ok := strings.Cut(cookie.Value, ":")
	if !ok {
		return nil
	}
	n, convErr := strconv.Atoi(count)
	if convErr != nil {
		return nil
	}
	return map[string]int{app: n}
}
