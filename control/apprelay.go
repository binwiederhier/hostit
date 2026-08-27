package control

import (
	"errors"
	"log/slog"
	"net/http"

	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
)

// The receiving side of the node's app-socket relay.
//
// The app socket is served by hostit-node on every host (an app on a node-only
// machine used to have none at all -- that was the bug). The node authenticates
// the caller by SO_PEERCRED against its own mirror, then relays the request
// here over the cluster link, naming the app in a header. Control keeps every
// authorization decision: archived, powered off, policy -- the node never
// answers a /v1 request from its own machinery, because answering locally
// would bypass exactly those guards.
//
// The header is trustworthy for one reason only: this handler exists PER
// AUTHENTICATED NODE CONNECTION, reachable solely over that node's duplex
// session, and every request is checked against the apps that node actually
// hosts -- the same scoping link.CallbackHandler applies to snapshot and
// usage callbacks. The same header on any public surface is meaningless;
// nothing there reads it, and a test pins that.

// AppRelayHandler serves the app-facing /v1 surface for apps hosted on one
// node, resolving the app from the relay header instead of peer credentials.
func (s *Server) AppRelayHandler(nodeID string) http.Handler {
	mux := s.selfMux(func(next func(http.ResponseWriter, *http.Request, *store.App)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			name := r.Header.Get(nodeapi.AppRelayHeader)
			if name == "" {
				writeError(w, http.StatusBadRequest, errors.New("relay request names no app"))
				return
			}
			a, err := s.apps.Store().App(name)
			if err != nil {
				writeAppError(w, err)
				return
			}
			// A node may only speak for apps it hosts. Without this, a
			// compromised node could drive any app in the cluster by naming it.
			host := a.Host
			if host == "" {
				host = store.HostLocal // legacy rows predate the host column
			}
			if host != nodeID {
				slog.Warn("Rejecting app relay for an app the node does not host", "node", nodeID, "app", name)
				writeError(w, http.StatusForbidden, errors.New("app not hosted by this node"))
				return
			}
			next(w, r, a)
		}
	})
	return http.StripPrefix(nodeapi.AppRelayPrefix, mux)
}
