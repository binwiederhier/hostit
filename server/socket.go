package server

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"

	"golang.org/x/sys/unix"
	"heckel.io/hostit/store"
)

const (
	// defaultLogLines is returned by /v1/self/logs unless ?lines= is given
	defaultLogLines = 100
)

// peerUIDContextKey is the context key under which a connection's peer UID is stored
type peerUIDContextKey struct{}

// socketHandler returns the unix socket API handler used by the app-side CLI
func (s *Server) socketHandler() http.Handler {
	return s.socket
}

func (s *Server) newSocketHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/self", s.selfApp(s.handleSelf))
	mux.HandleFunc("POST /v1/self/ensure", s.selfApp(s.handleSelfEnsure))
	mux.HandleFunc("POST /v1/self/up", s.selfApp(s.handleSelfUp))
	mux.HandleFunc("POST /v1/self/down", s.selfApp(s.handleSelfDown))
	mux.HandleFunc("POST /v1/self/restart", s.selfApp(s.handleSelfRestart))
	mux.HandleFunc("GET /v1/self/status", s.selfApp(s.handleSelfStatus))
	mux.HandleFunc("GET /v1/self/logs", s.selfApp(s.handleSelfLogs))
	return mux
}

// handleSelf tells the calling app who it is; this is how the CLI learns its
// name, port and URL without any token
func (s *Server) handleSelf(w http.ResponseWriter, r *http.Request, a *store.App) {
	writeJSON(w, http.StatusOK, s.appResponse(a))
}

func (s *Server) handleSelfEnsure(w http.ResponseWriter, r *http.Request, a *store.App) {
	msg, err := s.apps.Ensure(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

func (s *Server) handleSelfUp(w http.ResponseWriter, r *http.Request, a *store.App) {
	msg, err := s.apps.Up(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

func (s *Server) handleSelfDown(w http.ResponseWriter, r *http.Request, a *store.App) {
	if err := s.apps.Down(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "stopped"})
}

func (s *Server) handleSelfRestart(w http.ResponseWriter, r *http.Request, a *store.App) {
	if err := s.apps.Restart(a.Name); err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "restarted"})
}

func (s *Server) handleSelfStatus(w http.ResponseWriter, r *http.Request, a *store.App) {
	out, err := s.apps.Status(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiOutputResponse{Output: out})
}

func (s *Server) handleSelfLogs(w http.ResponseWriter, r *http.Request, a *store.App) {
	lines := defaultLogLines
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			lines = n
		}
	}
	out, err := s.apps.Logs(a.Name, lines)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiOutputResponse{Output: out})
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
