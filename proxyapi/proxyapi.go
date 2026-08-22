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
package proxyapi

import (
	"errors"

	"heckel.io/hostit/hoststats"
)

var (
	// ErrNoCert means control manages no certificate for that name -- an
	// unknown host, not a failure to reach control. The proxy answers the
	// handshake from its cache (or a self-signed stand-in) either way, but only
	// a reachable control can tell it the name is genuinely unknown.
	ErrNoCert = errors.New("no certificate for that name")
)

// Route maps one hostname to its forwarding target. Control's own hostnames
// (the dashboard, the API) are not listed: unknown hosts fall through to
// control anyway, so listing them would only duplicate the fallback.
type Route struct {
	Host   string `json:"host"`
	Target string `json:"target"` // host:port the app listens on
	// Private means the app is not served straight from here: the request goes
	// to control, which is where sessions and grants are understood. The proxy
	// holds no session key by design, so it cannot tell one visitor from
	// another and must not try.
	Private bool `json:"private,omitempty"`
}

// Table is the whole routing table at one point in time. Seq strictly
// increases with every change, so a proxy can tell control's newer word from
// an older one that arrived late.
type Table struct {
	Seq    int64   `json:"seq"`
	Routes []Route `json:"routes"`
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
	Stats hoststats.Stats `json:"stats"`
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
