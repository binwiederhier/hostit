package proxy

import (
	"log/slog"
	"net/http"
	"strings"

	"heckel.io/hostit/appgrant"
	"heckel.io/hostit/proxyapi"
)

// Private apps are served from here, not from control, and that is deliberate:
// the proxy exists so apps keep answering when the control plane does not, and
// a private app that dies with control would have given that up for the apps
// that need it most.
//
// Two things arrive with the routing table and make it possible. The grant
// PUBLIC key, which checks the cookie a visitor carries -- Ed25519, so holding
// it can never become the power to issue one. And the access set control
// resolved for each private app, so the proxy answers "is this id allowed?"
// with a lookup rather than a question it would need control to answer.
//
// Only the ALLOW path lives here. A refusal, a sign-in bounce, a logout and a
// bearer token all still fall through to control, which owns the error page,
// the session cookie and the token table. So this adds one decision, not a
// second copy of the policy.

const (
	// grantCookieName must match control's appGrantCookieName. The __Host-
	// prefix is added by control when it sets the cookie; a request carries
	// either name, and both are checked here rather than requiring the proxy to
	// know whether TLS is on.
	grantCookieName     = "hostit_app"
	grantCookieNameHost = "__Host-" + grantCookieName
)

// mayServePrivately reports whether this request proves, on its own, that its
// sender may open the app -- and if so, strips the proof before the request is
// forwarded, so the app never sees the credential that let its visitor in.
func (p *Proxy) mayServePrivately(r *http.Request, route proxyapi.Route, table *proxyapi.Table) bool {
	// A bearer token is checked against hashes that are not in the table, so
	// judging it here would mean guessing. Control still owns that call.
	if r.Header.Get("Authorization") != "" {
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
	if app != route.App || !allowed(userID, route.Access, table.Admins) {
		return false
	}
	stripGrantCookie(r)
	return true
}

func allowed(userID string, access, admins []string) bool {
	for _, ids := range [][]string{access, admins} {
		for _, id := range ids {
			if id == userID {
				return true
			}
		}
	}
	return false
}

func grantCookie(r *http.Request) string {
	for _, name := range []string{grantCookieNameHost, grantCookieName} {
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
		if c.Name != grantCookieName && c.Name != grantCookieNameHost {
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
