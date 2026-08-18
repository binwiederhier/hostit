package cluster

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// The same-host transport. A member sharing control's machine reaches it over a
// unix socket instead of mTLS on loopback, and the kernel says who is calling.
//
// This is not a shortcut, it is the smaller trust story. Certificates exist to
// authenticate a peer across a network; between two processes on one host the
// filesystem already does that, and better: a 0600 socket only root can open is
// a narrower surface than a private key on disk that anything running as root
// can read anyway. It also removes a bootstrap state -- control minting
// credentials for itself, which members then had to wait to appear on disk --
// and that gap is what put a proxy on :443 with no certificates once.

const (
	// socketScheme marks a control address as a path rather than host:port.
	socketScheme = "unix:"
	// DefaultSocketFile is where control accepts same-host members.
	DefaultSocketFile = "/run/hostit/cluster.sock"
)

// peerUIDKey carries the connecting process's uid from the listener down to the
// handler; http.Request has no way to reach its own connection.
type peerUIDKey struct{}

// IsSocketAddr reports whether an address names a unix socket.
func IsSocketAddr(addr string) bool {
	return strings.HasPrefix(addr, socketScheme)
}

// SocketPath strips the scheme from a socket address.
func SocketPath(addr string) string {
	return strings.TrimPrefix(addr, socketScheme)
}

// ListenSocket creates control's member socket, replacing a stale one from a
// previous process. Mode 0600: only root connects, which is the whole
// authentication story on this path.
func ListenSocket(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// A socket file outlives the process that made it; a leftover one would make
	// Listen fail with "address already in use" on every restart.
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

// DialSocket connects to control's member socket.
func DialSocket(path string) (net.Conn, error) {
	return net.Dial("unix", path)
}

// SocketServer serves handler on a unix listener, tagging every request with
// the connecting process's uid. The uid is what authorizes the caller here, so
// it has to reach the handler: a header cannot, since anything that can open
// the socket can write any header it likes.
func SocketServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler: handler,
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			uid, err := peerUID(c)
			if err != nil {
				return ctx // no credentials: the handler refuses
			}
			return context.WithValue(ctx, peerUIDKey{}, uid)
		},
	}
}

// peerUID asks the kernel who is on the other end.
func peerUID(c net.Conn) (int, error) {
	unixConn, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("not a unix connection")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *syscall.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return int(cred.Uid), nil
}

// trustedPeerUID says whether a process with this uid may claim an identity on
// the socket: the user control itself runs as, or root. Not "root only" --
// control is meant to stop needing root, and a check that assumed otherwise
// would have to be found and fixed at exactly the wrong moment. Root is
// admitted because root can read the socket, the database and the credentials
// regardless; refusing it would be theatre.
func trustedPeerUID(uid int) bool {
	return uid == os.Getuid() || uid == 0
}

// socketPeerUID returns the uid the listener recorded for this request.
func socketPeerUID(r *http.Request) (int, bool) {
	uid, ok := r.Context().Value(peerUIDKey{}).(int)
	return uid, ok
}
