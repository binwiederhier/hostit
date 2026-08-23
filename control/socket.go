package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"

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
// selfPrefixes are the two roots the app-facing surface answers at.
//
// /api/self is the one to write down: it reads the way the rest of the API
// does, where /v1 next to /api was always an odd seam. /v1 keeps answering and
// always will -- it is what the in-container CLI calls and what every app
// written so far uses, and there is no version of this worth breaking an app
// over.
var selfPrefixes = []string{"/v1", "/api/self"}

func (s *Server) selfMux(wrap func(func(http.ResponseWriter, *http.Request, *store.App)) http.HandlerFunc) *http.ServeMux {
	mux := http.NewServeMux()
	// Registered under both roots. The paths below keep saying /v1 so the
	// diff against what apps call stays readable; handle() adds the other.
	handle := func(pattern string, h http.HandlerFunc) {
		method, path, _ := strings.Cut(pattern, " ")
		for _, prefix := range selfPrefixes {
			mux.HandleFunc(method+" "+prefix+strings.TrimPrefix(path, "/v1"), h)
		}
	}
	handle("GET /v1/self", wrap(s.handleSelf))
	// The connections and credentials this app was granted, and a usable token
	// for one, addressed by the slug its owner gave it. This is what lets an app
	// read its owner's calendar without ever holding a refresh token or an
	// environment variable nothing can rotate.
	handle("GET /v1/connections", wrap(s.handleSelfConnectionsList))
	handle("GET /v1/connections/{slug}/token", wrap(s.handleSelfConnectionToken))
	// MCP servers are called THROUGH hostit rather than handed over as a token:
	// an MCP credential opens the whole server, so giving it to the app would
	// make the grant decorative. The app sends a tool name and arguments.
	handle("GET /v1/mcp/{slug}/tools", wrap(s.handleSelfMCPTools))
	handle("POST /v1/mcp/{slug}/call", wrap(s.handleSelfMCPCall))
	handle("POST /v1/self/ensure", wrap(s.handleSelfEnsure)) // SSH login provisions the workspace
	// The same lifecycle verbs the web app and admin CLI use, split into the app
	// process (start/stop/restart) and its container (poweron/poweroff/reboot).
	handle("POST /v1/self/deploy", wrap(s.handleSelfDeploy))
	handle("POST /v1/self/start", wrap(s.handleSelfStart))
	handle("POST /v1/self/stop", wrap(s.handleSelfStop))
	handle("POST /v1/self/restart", wrap(s.handleSelfRestart))
	handle("POST /v1/self/poweron", wrap(s.handleSelfPowerOn))
	handle("POST /v1/self/poweroff", wrap(s.handleSelfPowerOff))
	handle("POST /v1/self/reboot", wrap(s.handleSelfReboot))
	handle("GET /v1/self/status", wrap(s.handleSelfStatus))
	handle("GET /v1/self/logs", wrap(s.handleSelfLogs))
	// One app-scoped tool call (the sandboxed Claude Max backend reaches its tools
	// through here, over the same peercred-authenticated socket the app CLI uses).
	handle("POST /v1/self/tool/{name}", wrap(s.handleSelfTool))
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
