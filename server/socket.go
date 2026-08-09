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
	mux.HandleFunc("POST /v1/self/ensure", s.selfApp(s.handleSelfEnsure)) // SSH login provisions the workspace
	// The same lifecycle verbs the web app and admin CLI use, split into the app
	// process (start/stop/restart) and its container (poweron/poweroff/reboot).
	mux.HandleFunc("POST /v1/self/deploy", s.selfApp(s.handleSelfDeploy))
	mux.HandleFunc("POST /v1/self/start", s.selfApp(s.handleSelfStart))
	mux.HandleFunc("POST /v1/self/stop", s.selfApp(s.handleSelfStop))
	mux.HandleFunc("POST /v1/self/restart", s.selfApp(s.handleSelfRestart))
	mux.HandleFunc("POST /v1/self/poweron", s.selfApp(s.handleSelfEnsure))
	mux.HandleFunc("POST /v1/self/poweroff", s.selfApp(s.handleSelfPowerOff))
	mux.HandleFunc("POST /v1/self/reboot", s.selfApp(s.handleSelfReboot))
	mux.HandleFunc("GET /v1/self/status", s.selfApp(s.handleSelfStatus))
	mux.HandleFunc("GET /v1/self/logs", s.selfApp(s.handleSelfLogs))
	return mux
}

// handleSelf tells the calling app who it is; this is how the CLI learns its
// name, port and URL without any token
func (s *Server) handleSelf(w http.ResponseWriter, r *http.Request, a *store.App) {
	writeJSON(w, http.StatusOK, s.appResponse(a, s.firstActiveDomain(a.Name)))
}

func (s *Server) handleSelfEnsure(w http.ResponseWriter, r *http.Request, a *store.App) {
	msg, err := s.apps.Ensure(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

// handleSelfDeploy applies hostit.yml and (re)starts the app
func (s *Server) handleSelfDeploy(w http.ResponseWriter, r *http.Request, a *store.App) {
	msg, err := s.apps.Up(a.Name)
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: msg})
}

// The app-process verbs: they move the run: command, leaving the container up.
func (s *Server) handleSelfStart(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.StartApp(a.Name), "app started")
}

func (s *Server) handleSelfStop(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.StopApp(a.Name), "app stopped")
}

func (s *Server) handleSelfRestart(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.RestartApp(a.Name), "app restarted")
}

// The container verbs: they move the whole app container.
func (s *Server) handleSelfPowerOff(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.Down(a.Name), "powered off")
}

func (s *Server) handleSelfReboot(w http.ResponseWriter, r *http.Request, a *store.App) {
	s.selfAction(w, s.apps.Restart(a.Name), "rebooting")
}

// selfAction writes a lifecycle result: the error, or a success message
func (s *Server) selfAction(w http.ResponseWriter, err error, ok string) {
	if err != nil {
		writeAppError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: ok})
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
