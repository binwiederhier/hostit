package screenshot

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"log/slog"
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
	// Ask chrome to report its own rendering milestones. This is the signal that
	// pixel-diffing can only approximate: firstContentfulPaint means content is
	// actually on screen, and networkIdle means the page stopped fetching. A
	// browser that tells us it is done beats guessing from two matching frames.
	// Best-effort -- an older chrome without it just falls through to the poll.
	_, _ = c.call(ctx, "Page.setLifecycleEventsEnabled", map[string]any{"enabled": true})
	// Make the page the foreground target. A target chrome treats as hidden gets
	// a backgrounded renderer, which commits no frames -- the shot then waits out
	// its whole budget on a page that never paints. Best-effort, like the above.
	_, _ = c.call(ctx, "Page.bringToFront", nil)
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
	// Ask the browser when it is ready, rather than inferring it from pixels.
	// waitReady returns as soon as chrome reports content painted AND the network
	// quiet, so an animating page (which never yields two identical frames) is
	// done in seconds instead of burning the whole budget.
	// Waiting for a paint signal is capped well short of the settle budget: a
	// page that paints at all does so in the first seconds, and the fallback poll
	// then gets its own full budget. Worst case is paintSignalWait + settle +
	// settleGrace, comfortably inside the shot's own timeout.
	loadStart := time.Now()
	signalBudget := paintSignalWait
	if signalBudget > settle {
		signalBudget = settle
	}
	painted, idle := c.waitReady(ctx, navID, signalBudget)
	loadWait := time.Since(loadStart)
	if ctx.Err() != nil {
		return nil, ctx.Err() // the whole shot was cancelled, not just the wait
	}
	// Content painted is the authority: chrome says the page put something on
	// screen, so give it the flat grace to finish (a late font, a lazy image, a
	// chart drawing after its data lands) and store what is there. No pixel
	// diffing and no blank check -- a pale page that really did paint is a real
	// page, and refusing it would leave it with no preview at all.
	if painted {
		final, err := c.captureOnce(ctx)
		if err != nil {
			return nil, err
		}
		final = c.afterGrace(ctx, final)
		slog.Debug("Preview shot ready", "url", pageURL, "load_wait", loadWait.Round(time.Millisecond),
			"network_idle", idle, "outcome", "painted")
		return final, nil
	}
	// No paint signal: fall back to watching the pixels, below. That covers a
	// chrome too old for lifecycle events and a page that never reports one.
	slog.Info("Preview shot got no paint signal; falling back to frame polling",
		"url", pageURL, "waited", loadWait.Round(time.Millisecond), "network_idle", idle)

	// The settle: poll for a STABLE frame instead of shooting once after a fixed
	// delay. Capture every settlePoll and return the first frame identical to the
	// one before it -- the page has stopped changing (finished its first render,
	// fonts, images, the data an SPA fetches after mount). This is what a fixed
	// delay could not do: it photographed whatever was on screen at one instant,
	// so a still-painting page came out blank or HALF-rendered. A page that never
	// settles (an animating game) yields its latest frame when the budget runs
	// out. The poll never waits past the budget, so a short settle still returns
	// a single quick shot.
	settleStart := time.Now()
	deadline := settleStart.Add(settle)
	var prev, cur []byte
	polls := 0
	for {
		wait := settlePoll
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		cur, err = c.captureOnce(ctx)
		if err != nil {
			return nil, err
		}
		polls++
		if prev != nil && bytes.Equal(prev, cur) && !isMostlyBlank(cur) {
			// Two identical, non-blank frames: the page LOOKS settled. It is not
			// always finished, though -- a page can hold still between stages (a
			// font swapping in, a lazy image, a chart drawing after its data
			// lands), which the poll reads as "done" and photographs half-rendered.
			// So give it a flat grace and take the LAST frame: cheap insurance on
			// a preview that is only taken every few hours.
			final := c.afterGrace(ctx, cur)
			slog.Debug("Preview shot settled", "url", pageURL, "load_wait", loadWait.Round(time.Millisecond),
				"settle", time.Since(settleStart).Round(time.Millisecond), "polls", polls, "outcome", "settled")
			return final, nil
		}
		if !time.Now().Before(deadline) {
			// Budget spent. A frame that is STILL blank is not a preview -- handing
			// it back stores a white card over a good one, so fail and let the
			// caller keep what it had. A painted-but-still-changing page (an
			// animating game) is fine: its latest frame is a real picture.
			blank := isMostlyBlank(cur)
			slog.Info("Preview shot did not settle within its budget", "url", pageURL,
				"load_wait", loadWait.Round(time.Millisecond), "settle", time.Since(settleStart).Round(time.Millisecond),
				"polls", polls, "blank", blank)
			if blank {
				return nil, fmt.Errorf("page was still blank after %s; not storing an empty preview", settle)
			}
			return cur, nil
		}
		prev = cur
	}
}

// waitReady watches chrome's own lifecycle events and reports what the page
// achieved: painted is firstContentfulPaint (content is on screen), idle is
// networkIdle/networkAlmostIdle (it stopped fetching). It returns as soon as
// both are in, or when limit runs out with whatever it has -- a page holding a
// websocket open never reaches networkIdle, and that must not cost it its shot.
//
// This is the signal the frame-polling settle could only guess at, and it is why
// an animating page no longer waits out the whole budget: a canvas that never
// yields two identical frames still fires these events in the first second.
func (c *cdpConn) waitReady(ctx context.Context, cmdID int, limit time.Duration) (painted, idle bool) {
	deadline := time.After(limit)
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			return
		case msg, ok := <-c.msgs:
			if !ok {
				return
			}
			if msg.ID == cmdID && msg.Error != nil {
				return
			}
			// The load event alone does not prove anything was painted, but a page
			// with no lifecycle events at all (an old chrome) still fires it, and
			// treating it as "loaded, not painted" is what sends us to the poll.
			if msg.Method != "Page.lifecycleEvent" {
				continue
			}
			var p struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(msg.Params, &p) != nil {
				continue
			}
			switch p.Name {
			case "firstContentfulPaint", "firstMeaningfulPaint":
				painted = true
			case "networkIdle", "networkAlmostIdle":
				idle = true
			}
			if painted && idle {
				return
			}
		}
	}
}

// afterGrace waits a flat settleGrace and re-captures, returning the newer frame
// when it is usable. It is the guard against a page that merely PAUSED -- held
// still long enough to look settled, then painted the rest (a late font, a lazy
// image, a chart that draws once its data lands). Best-effort by design: any
// failure, or a frame that came back blank, keeps the frame we already had.
func (c *cdpConn) afterGrace(ctx context.Context, settled []byte) []byte {
	select {
	case <-ctx.Done():
		return settled
	case <-time.After(settleGrace):
	}
	later, err := c.captureOnce(ctx)
	if err != nil || isMostlyBlank(later) {
		return settled
	}
	return later
}

// captureOnce takes one screenshot and returns the decoded PNG bytes.
func (c *cdpConn) captureOnce(ctx context.Context) ([]byte, error) {
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

// isMostlyBlank reports whether a PNG is effectively a blank white frame -- what
// chrome hands back before an app has painted, or when the shot could not reach
// it. Used so the settle does not accept a stable-but-blank frame and stop early;
// it keeps polling until the app paints or the budget runs out. An undecodable
// image is treated as NOT blank, so a decode quirk never makes a shot wait out
// the whole budget.
func isMostlyBlank(pngBytes []byte) bool {
	img, err := png.Decode(bytes.NewReader(pngBytes))
	if err != nil {
		return false
	}
	b := img.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return true
	}
	// Sample a DENSE grid: text is thin strokes, and a coarse grid slips between
	// them -- a real page of prose then reads as empty. Every other pixel keeps
	// the work small while making sure anything painted is actually seen.
	const step = 2
	var sampled, ink int
	for y := b.Min.Y; y < b.Max.Y; y += step {
		for x := b.Min.X; x < b.Max.X; x += step {
			r, g, bl, _ := img.At(x, y).RGBA() // 16-bit per channel
			sampled++
			if r < 0xf800 || g < 0xf800 || bl < 0xf800 { // anything not ~white
				ink++
			}
		}
	}
	if sampled == 0 {
		return true
	}
	// Blank means NOTHING was painted, not "mostly white". A sparse page -- a
	// heading and two lines on white, which most small apps are -- covers a
	// fraction of a percent, and calling that blank fails its shot and costs it
	// its preview. Only a frame with essentially no ink at all (<= 0.02%, i.e. a
	// handful of antialiasing pixels) counts as empty.
	return ink*10000/sampled <= 2
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
