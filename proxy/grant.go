package proxy

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"heckel.io/hostit/appgrant"
	"heckel.io/hostit/proxyapi"
)

// Private apps are served from here rather than from control, so they keep
// answering when control does not. Two things arriving with the routing table
// make that possible: the grant PUBLIC key, which checks a visitor's cookie
// without being able to issue one, and the access set control resolved for
// each private app. Only the ALLOW path lives here -- everything else falls
// through to control, which owns the error page and the credentials.

// The bare name is only honoured without TLS, where control cannot use the
// __Host- prefix either. Under TLS the prefix is the whole guarantee: apps are
// subdomains of the web hostname, so any tenant can set a bare cookie with a
// Domain that reaches every other app, and only the prefixed name is one a
// browser refuses to scope that way.
var grantCookieNames = []string{proxyapi.GrantCookieHost, proxyapi.GrantCookie}

// hostitPaths are answered by control even on a private app whose visitor is
// allowed in: signing out of an app cannot be the app's own business.
var hostitPaths = []string{proxyapi.AuthPath, proxyapi.GrantedPath, proxyapi.LogoutPath}

// mayServePrivately reports whether this request proves, on its own, that its
// sender may open the app -- and if so, strips the proof before the request is
// forwarded, so the app never sees the credential that let its visitor in.
func (p *Proxy) mayServePrivately(r *http.Request, route proxyapi.Route, table *proxyapi.Table) bool {
	// A bearer token is checked against hashes that are not in the table, so
	// judging it here would mean guessing. Control still owns that call.
	if r.Header.Get("Authorization") != "" {
		return false
	}
	// hostit's own endpoints on this hostname are control's, grant or no grant.
	// Forwarding /hostit/logout to the app would 404 there and leave the cookie
	// in place, breaking sign-out exactly when it is used.
	if slices.Contains(hostitPaths, r.URL.Path) {
		return false
	}
	value := grantCookie(r)
	if value == "" || table.GrantPublicKey == "" {
		return false
	}
	verifier, err := appgrant.NewVerifier(table.GrantPublicKey)
	if err != nil {
		slog.Warn("Cannot use the grant key control pushed", "error", err)
		return false
	}
	app, userID, err := verifier.Verify(value)
	if err != nil {
		return false
	}
	// The grant names an app; this route may be a custom domain for it, so the
	// name is compared against the route's app rather than its hostname.
	// An empty id is not an identity: an ownerless app and a token-authenticated
	// grant both carry one, and matching them against each other would be an
	// accident rather than a decision.
	if userID == "" || app != route.App || !allowed(userID, route.Access, table.Admins) {
		return false
	}
	stripGrantCookie(r)
	return true
}

func allowed(userID string, access, admins []string) bool {
	return slices.Contains(access, userID) || slices.Contains(admins, userID)
}

func grantCookie(r *http.Request) string {
	names := grantCookieNames
	if r.TLS != nil {
		names = names[:1] // the proxy terminates TLS, so it knows
	}
	for _, name := range names {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// stripGrantCookie removes the grant from the Cookie header, leaving the app's
// own cookies alone.
func stripGrantCookie(r *http.Request) {
	cookies := r.Cookies()
	kept := make([]string, 0, len(cookies))
	for _, c := range cookies {
		if !slices.Contains(grantCookieNames, c.Name) {
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
