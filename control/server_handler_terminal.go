package control

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"heckel.io/hostit/appctl"
)

const (
	// terminalMaxDuration caps a single browser terminal session, so a tab left
	// open forever does not hold a shell (and a podman exec) on the host forever.
	terminalMaxDuration = 60 * time.Minute
	// terminalReadLimit bounds a single websocket message; terminal input arrives
	// in tiny frames, so anything large is a client bug or abuse.
	terminalReadLimit = 64 * 1024
	// terminalStatusPoweredOff is the close code the browser terminal receives when
	// the app is powered off. Unlike an ordinary drop, the client shows a note and
	// does NOT reconnect: a reconnect would be refused and must never power the app
	// back on. 4001 is in the WebSocket application-private code range.
	terminalStatusPoweredOff = websocket.StatusCode(4001)
	// terminalStatusArchived is the close code for an archived app. Distinct from
	// powered-off because it is more final: the app refuses to start until it is
	// unarchived, so the browser must stop retrying rather than count down
	// forever against something that cannot succeed.
	terminalStatusArchived = websocket.StatusCode(4002)
)

// handleTerminal bridges a browser terminal to an interactive shell in the app's
// container. It is registered under requireActive and resolves an owned app, so
// only the owner (or an admin) reaches it; the shell runs as the container's own
// mapped user, exactly like an SSH session. The origin check on the upgrade stops
// another site -- including a tenant's own app page -- from opening one on a
// signed-in user's behalf.
func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request, c *caller) {
	a, err := s.ownedApp(c, r.PathValue("name"))
	if err != nil {
		writeAppError(w, err)
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: s.config.WebHostnames(), // only our own web origins may connect
	})
	if err != nil {
		return // Accept has already written the failure
	}
	defer conn.CloseNow()
	conn.SetReadLimit(terminalReadLimit)

	// Opening a terminal must not power a stopped app back on if it was deliberately
	// powered off -- that would defeat poweroff, and an auto-reconnecting terminal
	// would fight the operator. Ensure gates that: it starts a crashed/enabled app
	// as before, but returns ErrPoweredOff for a disabled one, which we relay with a
	// distinct close code so the client stops reconnecting.
	if _, err := s.node.Ensure(a.Name); err != nil {
		// Logged, not just closed: the browser only sees a close code, and a
		// wrongly-refused terminal (seen once on stage, cause invisible) is
		// undiagnosable without the server-side reason.
		slog.Warn("Terminal refused: cannot ensure app", "app", a.Name, "error", err)
		if errors.Is(err, ErrArchived) {
			conn.Close(terminalStatusArchived, "app is archived")
			return
		}
		if errors.Is(err, appctl.ErrPoweredOff) {
			conn.Close(terminalStatusPoweredOff, "app is powered off")
			return
		}
		conn.Close(websocket.StatusInternalError, "cannot start app")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), terminalMaxDuration)
	defer cancel()

	// The pty runs ON THE APP'S NODE -- the only machine where the app's user
	// exists -- and arrives here as a byte stream over the cluster link (or
	// in-process for a colocated node). Control used to exec the shell itself,
	// which worked exactly as long as every app was on control's own host.
	session, err := s.node.Terminal(a.Name)
	if err != nil {
		slog.Warn("Terminal refused: cannot start shell", "app", a.Name, "error", err)
		conn.Close(websocket.StatusInternalError, "cannot start shell")
		return
	}
	defer session.Close()

	// Shell output -> browser
	go func() {
		defer cancel()
		buf := make([]byte, 4096)
		for {
			n, err := session.Read(buf)
			if n > 0 {
				if conn.Write(ctx, websocket.MessageBinary, buf[:n]) != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// Browser -> shell. A text frame is a resize control message; binary is input.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var rs struct {
				Cols uint16 `json:"cols"`
				Rows uint16 `json:"rows"`
			}
			if json.Unmarshal(data, &rs) == nil && rs.Cols > 0 && rs.Rows > 0 {
				_ = session.Resize(rs.Cols, rs.Rows)
			}
			continue
		}
		if _, err := session.Write(data); err != nil {
			return
		}
	}
}
