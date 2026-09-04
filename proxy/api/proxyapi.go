// Package proxyapi is the contract between the control plane and a proxy: the
// ProxyAgent verbs control calls to hand a proxy its configuration, the
// ControlSink callback a proxy reports back through, and the shapes that cross
// the wire between them.
//
// It is deliberately tiny and dependency-free, which is the point: a proxy
// terminates TLS and forwards bytes, so the only thing it needs to be told is
// where each hostname goes. Compare nodeapi, which is a machine's whole
// surface -- a proxy holds no registry, provisions nothing, and owns no state
// beyond a cache of what control last said.
package api

import (
	"errors"

	"heckel.io/hostit/system/stats"
)

var (
	// ErrNoCert means control manages no certificate for that name -- an
	// unknown host, not a failure to reach control. The proxy answers the
	// handshake from its cache (or a self-signed stand-in) either way, but only
	// a reachable control can tell it the name is genuinely unknown.
	ErrNoCert = errors.New("no certificate for that name")
)

const (
	// GrantCookie is the per-app grant a visitor to a private app carries on
	// that app's own hostname; GrantCookieHost is the name control uses where
	// the browser will accept the __Host- prefix. Both ends need these, so they
	// live in the contract rather than being spelled out twice.
	GrantCookie     = "hostit_app"
	GrantCookieHost = "__Host-" + GrantCookie
	// PreviewPrincipal is the reserved "user" named by the app-bound grant that
	// control's screenshot browser presents to reach a PRIVATE app. Because the
	// grant names the app, it opens ONLY that app -- so the proxy honours it
	// without the principal appearing in any app's access set.
	PreviewPrincipal = "__hostit_preview__"
)

// The paths hostit answers for ON a private app's own hostname, ahead of the
// app itself. The proxy has to know them too: it serves private apps directly,
// and must hand these back to control rather than to the app.
const (
	AuthPath    = "/hostit/auth"
	GrantedPath = "/hostit/granted"
	LogoutPath  = "/hostit/logout"
)

// The base security headers every public response carries, tenant apps
// included. Defined here because BOTH gates serve app traffic -- control in a
// single-host deployment, the proxy everywhere else -- and a response should
// not depend on which one answered it.
const (
	HSTSValue          = "max-age=63072000; includeSubDomains"
	ContentTypeOptions = "nosniff"
	ReferrerPolicy     = "strict-origin-when-cross-origin"
)

// ForwardingHeaders are the client-supplied headers a front door DROPS before
// forwarding, so an app can never be told a false client address. The reverse
// proxy sets X-Forwarded-For/Host/Proto itself, but every other forwarding
// header a client invents is passed through untouched -- and plenty of
// frameworks read X-Real-IP or Forwarded as the client address, which would
// make a tenant's own rate limits, allowlists and audit logs spoofable through
// hostit's front door. Listed here for the same reason as the headers above:
// both gates serve app traffic and must agree.
var ForwardingHeaders = []string{
	"Forwarded",
	"X-Real-Ip",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Forwarded-Port",
	"X-Forwarded-Prefix",
	"X-Forwarded-Server",
	"X-Original-Forwarded-For",
	"X-Client-Ip",
	"True-Client-Ip",
	"CF-Connecting-Ip",
}

// Route maps one hostname to its forwarding target. Control's own hostnames
// (the dashboard, the API) are not listed: unknown hosts fall through to
// control anyway, so listing them would only duplicate the fallback.
type Route struct {
	Host   string `json:"host"`
	Target string `json:"target"` // host:port the app listens on
	// App is the app this hostname belongs to. A grant names an app, not a
	// hostname, so a custom domain -- which says nothing about which app it
	// serves -- still needs this to be checkable.
	App string `json:"app,omitempty"`
	// Private means only certain people may open the app. The proxy enforces
	// that itself, from Access below, so a private app keeps serving while
	// control is down -- which is the whole reason the proxy holds a table.
	Private bool `json:"private,omitempty"`
	// Access is the user ids allowed to open a private app, besides admins:
	// its owner, its collaborators and its viewers, already resolved to active
	// accounts. Control evaluates the policy; the proxy only asks whether the
	// id in a verified grant is in this set, so there is one place the rule
	// lives and no way for the two to drift.
	Access []string `json:"access,omitempty"`
}

// Table is the whole routing table at one point in time. Seq strictly
// increases with every change, so a proxy can tell control's newer word from
// an older one that arrived late.
type Table struct {
	// GrantPublicKey verifies the per-app grant cookies visitors carry. It is
	// the PUBLIC half (Ed25519): the proxy can check a grant and can never mint
	// one, so holding the routing table does not become the power to hand out
	// access. Safe to persist alongside the routes.
	GrantPublicKey string `json:"grant_public_key,omitempty"`
	// Admins may open any app; global rather than repeated in every Access set.
	Admins []string `json:"admins,omitempty"`
	Seq    int64    `json:"seq"`
	Routes []Route  `json:"routes"`
}

// CertMaterial is one hostname's TLS material: the chain and its private key,
// PEM-encoded. It crosses only the cluster's mTLS connection.
type CertMaterial struct {
	CertPEM string `json:"cert_pem"`
	KeyPEM  string `json:"key_pem"`
}

// Heartbeat is what a proxy reports about itself when control asks: which
// build it runs and, by answering at all, that it is alive.
type Heartbeat struct {
	Version string `json:"version"`
	// Stats is the machine's own resource state; see nodeapi's Heartbeat.
	Stats stats.Stats `json:"stats"`
	// Routes is how many routes the proxy is currently serving, so control can
	// see a proxy that is connected but serving a stale or empty table.
	Routes int `json:"routes"`
}

// ProxyAgent is what control calls on a proxy. Control is the brain: it states
// what the proxy should be serving and the proxy applies it, the same way a
// node is handed its desired state rather than asked what it has.
type ProxyAgent interface {
	// ApplyRoutes replaces the proxy's routing table wholesale. Control sends
	// it on connect, on every change, and on the reconcile timer, so a proxy
	// that missed a push converges at the next one.
	ApplyRoutes(table *Table) error
	// Heartbeat reports the proxy's build and how much it is serving.
	Heartbeat() *Heartbeat
}

// ControlSink is the reverse direction: what a proxy asks control for over the
// same connection. Certificates are pulled rather than pushed because the
// trigger is a TLS handshake for a name the proxy has never seen, which is
// exactly when control may still need to issue one.
type ControlSink interface {
	// CertFor returns the material for one SNI name, issuing on demand for a
	// custom domain nobody has asked for yet. ErrNoCert means the name is not
	// ours.
	CertFor(sni string) (*CertMaterial, error)
}
