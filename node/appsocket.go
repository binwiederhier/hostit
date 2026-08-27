package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/node/link"
	"heckel.io/hostit/nodeapi"
	"heckel.io/hostit/store"
)

// The app socket, served by the NODE on every host.
//
// This used to be control's listener, which meant a host running only
// hostit-node had no socket at all: apps placed there lost SSH (the login
// shell calls the socket before greeting anyone) and the whole in-container CLI,
// and the MCP bridge. Serving it here -- on every host, not
// just where control is absent -- keeps one code path for both deployment
// shapes, which is precisely what the old arrangement did not have.
//
// The node authenticates the caller by SO_PEERCRED against its own mirror
// registry (which carries each hosted app's uid) and RELAYS the request to
// control over the cluster link, naming the app in a header. It deliberately
// never answers from its own machinery: control is where the guards live, and
// a node that answered "deploy" locally would let an archived app deploy
// itself from inside its own container.

var (
	// errNoPeerCreds means the connection carried no SO_PEERCRED, which a unix
	// socket always has; seeing this indicates a non-unix listener.
	errNoPeerCreds = errors.New("no peer credentials")
	// errControlUnreachable is the 502 an app sees between control connections.
	errControlUnreachable = errors.New("control is unreachable; retry in a moment")
)

// appSocketPeerUIDKey carries the caller's uid from the listener to the handler.
type appSocketPeerUIDKey struct{}

// ListenAppSocket opens the app socket: world-connectable (0666), because apps
// connect as their own uid -- the peer credential is the authentication, not
// the ability to open the file.
func ListenAppSocket(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// A socket file outlives its process; a leftover would fail the Listen.
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o666); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// AppSocketServer builds the http.Server for the app socket: peer uid captured
// per connection, requests resolved and relayed by appSocketHandler.
func AppSocketServer(st *store.Store, link *link.ControlLink) *http.Server {
	return &http.Server{
		Handler: appSocketHandler(st, link),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			uid, err := appSocketPeerUID(c)
			if err != nil {
				return ctx // no credentials: the handler refuses
			}
			return context.WithValue(ctx, appSocketPeerUIDKey{}, uid)
		},
	}
}

// ServeAppSocket listens and serves until the listener closes. Started before
// the control dial loop: the socket must exist the moment a container starts,
// and a relay before the first connection answers 502 rather than "no such
// file", which is an error an app can retry.
func ServeAppSocket(path string, st *store.Store, link *link.ControlLink) (io.Closer, error) {
	listener, err := ListenAppSocket(path)
	if err != nil {
		return nil, err
	}
	server := AppSocketServer(st, link)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("App socket server failed", "socket", path, "error", err)
		}
	}()
	slog.Info("Serving the app socket", "socket", path)
	return listener, nil
}

// isOperatorPath reports whether a request is aimed at the operator API, which
// this socket does not serve.
//
// /api/container is the exception and must stay one: it is the app-facing surface's
// alias for /v1, the app talking about ITSELF on exactly this socket. Without
// the carve-out the guard swallows it, and only a REMOTE node shows that --
// control's own socket never passes through here, so a unit test against it
// looks perfectly healthy.
func isOperatorPath(path string) bool {
	if path == containerAliasPrefix || strings.HasPrefix(path, containerAliasPrefix+"/") {
		return false
	}
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// containerAliasPrefix mirrors control's /api/container root; kept here as a constant so
// the carve-out above is findable from both ends.
const containerAliasPrefix = "/api/container"

// appSocketHandler resolves the calling app and relays its request.
func appSocketHandler(st *store.Store, link *link.ControlLink) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Operator commands do not live on this socket any more: control's own
		// socket serves them, where peer uid 0 means admin. Saying so here is
		// for the old CLI on an upgraded host, which would otherwise get a 401
		// that looks like a token problem.
		if isOperatorPath(r.URL.Path) {
			writeSocketError(w, http.StatusNotImplemented,
				"operator commands moved to hostit-control (or pass --host/--token); this socket serves apps")
			return
		}
		uid, ok := r.Context().Value(appSocketPeerUIDKey{}).(int)
		if !ok {
			writeSocketError(w, http.StatusForbidden, errNoPeerCreds.Error())
			return
		}
		// The mirror holds exactly the apps THIS node hosts, so an unknown uid
		// is refused here without asking control -- including uid 0: root on
		// the host is not an app, and saying so beats a confusing lookup error.
		a, err := st.AppByUID(uid)
		if err != nil {
			if uid == 0 {
				writeSocketError(w, http.StatusForbidden,
					"this socket serves apps; operator commands go through hostit-control")
				return
			}
			writeSocketError(w, http.StatusForbidden, fmt.Sprintf("no app for uid %d on this node", uid))
			return
		}
		relay(w, r, link, a.Name)
	})
}

// relay forwards one request to control over the cluster link, verbatim except
// for the path prefix and the app header, and streams the response back.
func relay(w http.ResponseWriter, r *http.Request, link *link.ControlLink, appName string) {
	client := link.Client()
	if client == nil {
		writeSocketError(w, http.StatusBadGateway, errControlUnreachable.Error())
		return
	}
	url := "http://" + cluster.ControlID + nodeapi.AppRelayPrefix + r.URL.Path
	if r.URL.RawQuery != "" {
		url += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, url, r.Body)
	if err != nil {
		writeSocketError(w, http.StatusInternalServerError, err.Error())
		return
	}
	req.Header = r.Header.Clone()
	req.Header.Set(nodeapi.AppRelayHeader, appName)
	resp, err := client.Do(req)
	if err != nil {
		writeSocketError(w, http.StatusBadGateway, errControlUnreachable.Error())
		return
	}
	defer resp.Body.Close()
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// writeSocketError answers in the same JSON shape control's socket uses, so the
// in-container CLI renders it identically whichever side refused.
func writeSocketError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, "{\"error\": %q}\n", message)
}

// appSocketPeerUID asks the kernel who is on the other end of the connection.
func appSocketPeerUID(c net.Conn) (int, error) {
	unixConn, ok := c.(*net.UnixConn)
	if !ok {
		return 0, errNoPeerCreds
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil || cred == nil {
		return 0, errNoPeerCreds
	}
	return int(cred.Uid), nil
}
