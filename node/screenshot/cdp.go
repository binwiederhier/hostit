package screenshot

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// Driving chrome over the DevTools protocol instead of its one-shot
// --screenshot mode, because one-shot mode only knows VIRTUAL time: chrome
// fast-forwards timers, so a page with an animation loop (a canvas, a game, a
// spinner -- most of what people host here) can burn a 60-second budget in a
// fraction of a real second and be captured before its images, fonts or first
// data have actually arrived. That is the intermittent white card.
//
// Real time is the fix: navigate, wait for the load event, then let the page
// settle for a fixed REAL interval, then capture what is on screen.

const (
	// cdpDialTimeout bounds waiting for chrome inside the container to open its
	// debugging port; it is a process start, not a page load.
	cdpDialTimeout = 20 * time.Second
	// cdpCallTimeout bounds one protocol round trip.
	cdpCallTimeout = 30 * time.Second
	// cdpReadLimit bounds one protocol message. A full-page PNG arrives
	// base64-encoded inside one, so this is generous on purpose.
	cdpReadLimit = 64 << 20
)

// cdpMessage is one DevTools protocol message, request or reply or event.
type cdpMessage struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// cdpConn is a page-scoped DevTools connection. Page-scoped on purpose: the
// browser-level endpoint would need target attachment and session routing for
// nothing -- the container starts with exactly one page.
//
// One reader goroutine feeds every consumer through msgs. It has to be this
// way: cancelling a websocket Read closes the connection (coder/websocket
// cannot resynchronise a half-read frame), so "wait for this event, but not
// forever" must be a select on a channel, never a Read with a deadline.
type cdpConn struct {
	ws      *websocket.Conn
	nextID  int
	msgs    chan *cdpMessage
	readErr error
}

// readLoop pumps messages until the connection dies; the error is kept for
// whoever is waiting when msgs closes.
func (c *cdpConn) readLoop(ctx context.Context) {
	defer close(c.msgs)
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.readErr = err
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			c.readErr = err
			return
		}
		select {
		case c.msgs <- &msg:
		case <-ctx.Done():
			c.readErr = ctx.Err()
			return
		}
	}
}

// pageWebSocketURL asks chrome for its one page target's debugger URL, retrying
// until chrome has finished starting (or ctx expires).
func pageWebSocketURL(ctx context.Context, debugBase string) (string, error) {
	deadline := time.Now().Add(cdpDialTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		url, err := fetchPageTarget(ctx, debugBase)
		if err == nil {
			return url, nil
		}
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	return "", fmt.Errorf("chrome did not open its debugging port: %w", lastErr)
}

func fetchPageTarget(ctx context.Context, debugBase string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", debugBase+"/json/list", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var targets []struct {
		Type string `json:"type"`
		URL  string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&targets); err != nil {
		return "", err
	}
	for _, t := range targets {
		if t.Type == "page" && t.URL != "" {
			return t.URL, nil
		}
	}
	return "", fmt.Errorf("chrome has no page target yet")
}

// capture navigates to pageURL, waits for the load event, lets the page settle
// for the given REAL duration, and returns the PNG bytes. A page that never
// fires load is still captured after the settle window: a stuck subresource
// must not cost the shot, and what is painted by then is what a visitor sees.
func capture(ctx context.Context, debugBase, pageURL string, settle time.Duration, cookie *http.Cookie) ([]byte, error) {
	wsURL, err := pageWebSocketURL(ctx, debugBase)
	if err != nil {
		return nil, err
	}
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot attach to chrome: %w", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "done")
	ws.SetReadLimit(cdpReadLimit)
	c := &cdpConn{ws: ws, msgs: make(chan *cdpMessage, 32)}
	readCtx, stopReading := context.WithCancel(ctx)
	defer stopReading()
	go c.readLoop(readCtx)

	if _, err := c.call(ctx, "Page.enable", nil); err != nil {
		return nil, err
	}
	// For a private app, seed the app-bound grant cookie before navigating, so
	// the very first request carries it and the proxy serves the app.
	if cookie != nil {
		if _, err := c.call(ctx, "Network.enable", nil); err != nil {
			return nil, err
		}
		if _, err := c.call(ctx, "Network.setCookie", map[string]any{
			"name": cookie.Name, "value": cookie.Value, "url": pageURL,
			"path": "/", "secure": cookie.Secure, "httpOnly": true, "sameSite": "Lax",
		}); err != nil {
			return nil, err
		}
	}
	navID, err := c.send(ctx, "Page.navigate", map[string]any{"url": pageURL})
	if err != nil {
		return nil, err
	}
	// Wait for load, but never longer than the settle window: a page that never
	// fires it is still worth capturing.
	if err := c.waitFor(ctx, navID, "Page.loadEventFired", settle); err != nil && ctx.Err() != nil {
		return nil, ctx.Err() // the whole shot was cancelled, not just the wait
	}

	// The settle: real seconds for the app to finish painting what the load
	// event does not cover -- fonts swapping in, a framework's first render,
	// images decoding, the data an SPA fetches after mount.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(settle):
	}

	res, err := c.call(ctx, "Page.captureScreenshot", map[string]any{"format": "png"})
	if err != nil {
		return nil, err
	}
	var shot struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(res, &shot); err != nil {
		return nil, err
	}
	png, err := base64.StdEncoding.DecodeString(shot.Data)
	if err != nil {
		return nil, fmt.Errorf("chrome returned an undecodable screenshot: %w", err)
	}
	if len(png) == 0 {
		return nil, fmt.Errorf("chrome returned an empty screenshot")
	}
	return png, nil
}

// send writes one command and returns its id, without waiting for the reply.
func (c *cdpConn) send(ctx context.Context, method string, params map[string]any) (int, error) {
	c.nextID++
	msg := map[string]any{"id": c.nextID, "method": method}
	if params != nil {
		msg["params"] = params
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return 0, err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, body); err != nil {
		return 0, fmt.Errorf("%s: %w", method, err)
	}
	return c.nextID, nil
}

// call sends a command and waits for its reply, skipping the events that
// arrive in between.
func (c *cdpConn) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	id, err := c.send(ctx, method, params)
	if err != nil {
		return nil, err
	}
	deadline := time.After(cdpCallTimeout)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("%s: timed out", method)
		case msg, ok := <-c.msgs:
			if !ok {
				return nil, fmt.Errorf("%s: connection closed: %w", method, c.readErr)
			}
			if msg.ID != id {
				continue // an event, or another command's reply
			}
			if msg.Error != nil {
				return nil, fmt.Errorf("%s: %s", method, msg.Error.Message)
			}
			return msg.Result, nil
		}
	}
}

// waitFor waits for the named event, giving up after limit -- the caller
// decides whether that is fatal. A failure reply to cmdID ends the wait early,
// since a refused navigation will never load.
func (c *cdpConn) waitFor(ctx context.Context, cmdID int, event string, limit time.Duration) error {
	deadline := time.After(limit)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("%s did not arrive", event)
		case msg, ok := <-c.msgs:
			if !ok {
				return fmt.Errorf("connection closed: %w", c.readErr)
			}
			if msg.ID == cmdID && msg.Error != nil {
				return fmt.Errorf("navigate: %s", msg.Error.Message)
			}
			if msg.Method == event {
				return nil
			}
		}
	}
}
