package server

import (
	"context"
	"errors"
	"net"
	"net/http"

	"golang.org/x/sys/unix"
)

// peerUIDContextKey is the context key under which a connection's peer UID is stored
type peerUIDContextKey struct{}

// socketHandler returns the unix socket API handler used by the app-side CLI
func (s *Server) socketHandler() http.Handler {
	return s.socket
}

func (s *Server) newSocketHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/self", s.handleSelf)
	return mux
}

// handleSelf identifies the calling app by the connection's peer UID and returns
// its app record; this is how "hostit up" learns its port and URL without a token
func (s *Server) handleSelf(w http.ResponseWriter, r *http.Request) {
	uid, ok := peerUID(r.Context())
	if !ok {
		writeError(w, http.StatusForbidden, errors.New("no peer credentials"))
		return
	}
	username, err := s.usernameForUID(uid)
	if err != nil {
		writeError(w, http.StatusForbidden, err)
		return
	}
	a, err := s.apps.App(username)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.appResponse(a, nil))
}

// socketConnContext stores the unix socket peer's UID (SO_PEERCRED) in the
// connection context, where handleSelf picks it up
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
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil || cred == nil {
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
