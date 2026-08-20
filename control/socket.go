package control

import (
	"context"
	"errors"
	"net"
	"net/http"

	"golang.org/x/sys/unix"
	"heckel.io/hostit/store"
)

// peerUIDContextKey is the context key under which a connection's peer UID is stored
type peerUIDContextKey struct{}

// socketHandler returns the unix socket API handler used by the app-side CLI
func (s *Server) socketHandler() http.Handler {
	return s.socket
}

func (s *Server) newSocketHandler() http.Handler {
	// Control's own socket resolves the app by the caller's peer uid. The same
	// /v1 surface is ALSO served per node over the cluster link (nodeRelayMux),
	// where the app is named by the node instead -- see AppRelayHandler.
	mux := s.selfMux(s.selfApp)
	// The operator API rides the same socket: authenticate() grants global admin
	// to peer uid 0, so root's CLI works without a token. Scoped to /api on
	// purpose; the web app and OAuth endpoints have no business on a local socket.
	mux.Handle(apiPrefix+"/", s.api)
	return mux
}

// selfMux registers the whole app-facing /v1 surface against a pluggable app
// resolver, so the same handlers serve control's own socket (peercred resolves
// the app) and the per-node relay (the authenticated node names the app). The
// routes exist once; only "who is asking" differs.
func (s *Server) selfMux(wrap func(func(http.ResponseWriter, *http.Request, *store.App)) http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/self", wrap(s.handleSelf))
	// The connected accounts this app was granted, and a usable token for one.
	// This is what makes an app able to read its owner's calendar without ever
	// holding a refresh token or an unrotatable environment variable.
	mux.HandleFunc("GET /v1/connections", wrap(s.handleSelfConnectionsList))
	mux.HandleFunc("GET /v1/connections/{provider}/token", wrap(s.handleSelfConnectionToken))
	mux.HandleFunc("POST /v1/self/ensure", wrap(s.handleSelfEnsure)) // SSH login provisions the workspace
	// The same lifecycle verbs the web app and admin CLI use, split into the app
	// process (start/stop/restart) and its container (poweron/poweroff/reboot).
	mux.HandleFunc("POST /v1/self/deploy", wrap(s.handleSelfDeploy))
	mux.HandleFunc("POST /v1/self/start", wrap(s.handleSelfStart))
	mux.HandleFunc("POST /v1/self/stop", wrap(s.handleSelfStop))
	mux.HandleFunc("POST /v1/self/restart", wrap(s.handleSelfRestart))
	mux.HandleFunc("POST /v1/self/poweron", wrap(s.handleSelfPowerOn))
	mux.HandleFunc("POST /v1/self/poweroff", wrap(s.handleSelfPowerOff))
	mux.HandleFunc("POST /v1/self/reboot", wrap(s.handleSelfReboot))
	mux.HandleFunc("GET /v1/self/status", wrap(s.handleSelfStatus))
	mux.HandleFunc("GET /v1/self/logs", wrap(s.handleSelfLogs))
	// One app-scoped tool call (the sandboxed Claude Max backend reaches its tools
	// through here, over the same peercred-authenticated socket the app CLI uses).
	mux.HandleFunc("POST /v1/self/tool/{name}", wrap(s.handleSelfTool))
	return mux
}

// selfApp resolves the calling app from the connection's peer UID (SO_PEERCRED);
// processes inside an app container carry the app's host UID through the
// mounted socket, so this works from inside and outside containers alike
func (s *Server) selfApp(next func(http.ResponseWriter, *http.Request, *store.App)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid, ok := peerUID(r.Context())
		if !ok {
			writeError(w, http.StatusForbidden, errors.New("no peer credentials"))
			return
		}
		// The registry knows every app's uid, including apps on other nodes;
		// the passwd file only knows this host's. Ask the registry first, and
		// keep the name lookup for rows predating the uid column.
		a, err := s.apps.Store().AppByUID(uid)
		if err != nil {
			username, nameErr := s.usernameForUID(uid)
			if nameErr != nil {
				writeError(w, http.StatusForbidden, nameErr)
				return
			}
			if a, err = s.apps.App(username); err != nil {
				writeAppError(w, err)
				return
			}
		}
		next(w, r, a)
	}
}

// socketConnContext stores the unix socket peer's UID (SO_PEERCRED) in the
// connection context, where selfApp picks it up
func socketConnContext(ctx context.Context, c net.Conn) context.Context {
	unixConn, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *unix.Ucred
	var credErr error
	err = raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil || credErr != nil || cred == nil {
		return ctx
	}
	return withPeerUID(ctx, int(cred.Uid))
}

func withPeerUID(ctx context.Context, uid int) context.Context {
	return context.WithValue(ctx, peerUIDContextKey{}, uid)
}

func peerUID(ctx context.Context) (int, bool) {
	uid, ok := ctx.Value(peerUIDContextKey{}).(int)
	return uid, ok
}
