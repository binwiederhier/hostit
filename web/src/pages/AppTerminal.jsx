import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { reconnectDelaySeconds, shouldReconnect, TERMINAL_ARCHIVED_CODE } from "../reconnect";

// A shell in the app's container, in the browser. The websocket streams raw
// terminal bytes both ways (binary), and a text frame carries the size whenever
// the window changes -- the server maps that onto the pty. It is the same login
// shell an SSH session gets (banner, colours, shortcuts), so it is owner-only on
// the server side.
//
// Two shapes: a floating panel on the app page (draggable, minimizable, and
// deliberately not dimming the page behind it so the live preview stays visible),
// and a full-window page reached by the pop-out (and directly at
// /app/<name>/terminal/popout).
const AppTerminal = ({ name, onClose, onMinimize, onReady, onSessionEnd, onSsh, minimized = false, fullPage = false, embedded = false, active = true }) => {
  // embedded fills its container inline (a workspace view); fullPage is the fixed
  // full-window overlay. Both drop the floating panel's drag/minimize chrome.
  const fixed = fullPage || embedded;
  const hostRef = useRef(null);
  const frameRef = useRef(null);
  const fitRef = useRef(null);
  const termRef = useRef(null);
  const sendSizeRef = useRef(() => {});
  // Reconnect state for the UI (countdown) and a handle to retry immediately.
  const reconnectNowRef = useRef(() => {});
  const [reconnect, setReconnect] = useState({ active: false, seconds: 0 });
  // Set when the server closed with the powered-off code: no automatic retries
  // (they would be refused), but the pane keeps a manual Reconnect button and
  // re-entering the tab retries once -- the app may have been powered on since.
  const stoppedRef = useRef(false);
  const [stopped, setStopped] = useState(false);
  // Where the floating panel sits; null until first dragged, so CSS places it.
  const [pos, setPos] = useState(null);

  useEffect(() => {
    const term = new Terminal({
      fontSize: 13,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      cursorBlink: true,
      theme: { background: "#14181d", foreground: "#e8ecf1", cursor: "#10b981" },
    });
    termRef.current = term;
    const fit = new FitAddon();
    fitRef.current = fit;
    term.loadAddon(fit);
    term.open(hostRef.current);
    fit.fit();

    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${window.location.host}/api/apps/${encodeURIComponent(name)}/terminal`;
    const encoder = new TextEncoder();
    let ws = null;
    let attempt = 0; // consecutive drops, for the backoff
    let retryTimer = null;
    let countdownTimer = null;
    let disposed = false;

    const sendSize = () => {
      fit.fit();
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }));
      }
    };
    sendSizeRef.current = sendSize;

    const clearTimers = () => {
      clearTimeout(retryTimer);
      clearInterval(countdownTimer);
      retryTimer = null;
      countdownTimer = null;
    };

    // After an unexpected drop, wait out the backoff (with a visible countdown),
    // then reconnect. The socket is reconnected in place; the xterm terminal and
    // its scrollback are kept, so a network blip or a container restart heals on
    // its own without losing the pane.
    const scheduleReconnect = () => {
      const secs = reconnectDelaySeconds(attempt);
      attempt += 1;
      let remaining = secs;
      setReconnect({ active: true, seconds: remaining });
      countdownTimer = setInterval(() => {
        remaining -= 1;
        setReconnect({ active: true, seconds: Math.max(0, remaining) });
      }, 1000);
      retryTimer = setTimeout(connect, secs * 1000);
    };

    function connect() {
      if (disposed) return;
      clearTimers();
      setReconnect({ active: false, seconds: 0 });
      // Detach the old socket's handlers first, so its onclose does not also try to
      // reconnect (double reconnect on a manual retry).
      if (ws) {
        ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
        try {
          ws.close();
        } catch {
          // already closing/closed
        }
      }
      ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      ws.onopen = () => {
        attempt = 0; // a clean connect resets the backoff
        // Every socket is a fresh login session: reset the screen so the new
        // banner does not stack under dead output -- in particular the yellow
        // "powered off" note a reconnect-after-poweron would otherwise keep.
        term.reset();
        stoppedRef.current = false;
        setStopped(false);
        sendSize();
        term.focus();
        if (onReady) {
          onReady(); // the shell is connected; the page can stop showing "connecting"
        }
      };
      ws.onmessage = (e) => term.write(new Uint8Array(e.data));
      ws.onclose = (event) => {
        if (disposed) return; // our own teardown (unmount)
        // A powered-off app closes with a distinct code: show a note and stop the
        // automatic retries, so an auto-reconnect never fights the poweroff (nor
        // powers the app back on). The manual Reconnect button stays, and coming
        // back to the tab retries once (see the active effect below) -- the app
        // may have been powered on in the meantime.
        if (!shouldReconnect(event.code)) {
          stoppedRef.current = true;
          setStopped(true);
          setReconnect({ active: false, seconds: 0 });
          // Say which of the two final states this is: powering on fixes one and
          // does nothing for the other.
          const why =
            event.code === TERMINAL_ARCHIVED_CODE
              ? "This app is archived. Unarchive it to use the terminal."
              : "This app is powered off. Power it on to use the terminal.";
          term.write(`\r\n\x1b[33m${why}\x1b[0m\r\n`);
          return;
        }
        scheduleReconnect();
      };
      ws.onerror = () => {}; // the close handler drives the reconnect; don't double-report
    }

    // Retry now: cancel any pending backoff and reconnect immediately.
    reconnectNowRef.current = () => {
      attempt = 0;
      connect();
    };

    connect();

    // Keepalive: re-assert the current size every 30s. A no-op for a healthy
    // session, but it exercises the browser->control->node path, so a dead
    // one (an app reboot that killed the pty without closing this socket)
    // surfaces as a socket error and the normal reconnect takes over instead
    // of the pane freezing silently.
    const keepalive = setInterval(() => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        sendSize();
      }
    }, 30000);

    const dataSub = term.onData((d) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(encoder.encode(d));
      }
    });
    const onResize = () => sendSize();
    window.addEventListener("resize", onResize);
    document.addEventListener("fullscreenchange", onResize);
    // The floating window is user-resizable (CSS resize), which the window resize
    // event does not cover: watch the element itself and refit on any size change.
    const ro = new ResizeObserver(() => sendSize());
    if (frameRef.current) {
      ro.observe(frameRef.current);
    }

    return () => {
      disposed = true;
      clearInterval(keepalive);
      window.removeEventListener("resize", onResize);
      document.removeEventListener("fullscreenchange", onResize);
      ro.disconnect();
      dataSub.dispose();
      clearTimers();
      if (ws) {
        ws.close();
      }
      term.dispose();
    };
  }, [name]);

  // Coming back from minimized, the window was display:none so xterm could not
  // measure it: re-measure and tell the pty once it is visible again.
  useEffect(() => {
    if (!minimized) {
      // Two frames: let the window lay out before xterm measures it.
      requestAnimationFrame(() => requestAnimationFrame(() => sendSizeRef.current()));
    }
  }, [minimized]);

  // Switching to the terminal tab: it was display:none, so re-measure and put the
  // cursor in it -- you can start typing without clicking first. If the last
  // close was the powered-off code, retry once here: the pane must not stay dead
  // after the owner powers the app back on and comes back to the tab.
  useEffect(() => {
    if (!active) return;
    if (stoppedRef.current) {
      reconnectNowRef.current();
    }
    requestAnimationFrame(() =>
      requestAnimationFrame(() => {
        sendSizeRef.current();
        termRef.current?.focus();
      })
    );
  }, [active]);

  const toggleFullscreen = () => {
    if (document.fullscreenElement) {
      document.exitFullscreen();
    } else if (frameRef.current) {
      frameRef.current.requestFullscreen().catch(() => {});
    }
  };
  const popOut = () => {
    window.open(`/app/${encodeURIComponent(name)}/terminal/popout`, "_blank", "width=960,height=600");
    if (onClose) {
      onClose();
    }
  };

  // Drag the panel by its title bar. We track the pointer's offset from the
  // window's top-left and keep the whole bar on screen.
  const startDrag = (e) => {
    if (fixed) return;
    const rect = frameRef.current?.getBoundingClientRect();
    if (!rect) return;
    const offX = e.clientX - rect.left;
    const offY = e.clientY - rect.top;
    const move = (ev) => {
      const left = Math.min(window.innerWidth - 80, Math.max(0, ev.clientX - offX));
      const top = Math.min(window.innerHeight - 40, Math.max(0, ev.clientY - offY));
      setPos({ left, top });
    };
    const up = () => {
      window.removeEventListener("pointermove", move);
      window.removeEventListener("pointerup", up);
      document.body.classList.remove("ws-resizing");
    };
    window.addEventListener("pointermove", move);
    window.addEventListener("pointerup", up);
    document.body.classList.add("ws-resizing");
  };

  const frame = (
    <div
      className={
        (fixed ? "term-window term-window-full" : "term-window term-window-float") +
        (!fixed && minimized ? " term-window-hidden" : "")
      }
      ref={frameRef}
      style={
        !fixed && pos
          ? { left: `${pos.left}px`, top: `${pos.top}px`, right: "auto", bottom: "auto", transform: "none" }
          : undefined
      }
    >
      <div className={fixed ? "term-bar" : "term-bar term-bar-drag"} onPointerDown={startDrag}>
        {!embedded && <span className="mono">{name} &mdash; terminal</span>}
        <span className="term-bar-actions">
          {/* Always available: a session can die without the socket closing
              (a reboot recreates the container under a pty that never EOFs
              here), and then reconnecting is the only cure. */}
          <button type="button" className="term-btn" onClick={() => reconnectNowRef.current()} title="Reload terminal" aria-label="Reload terminal">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M13 8a5 5 0 1 1-1.5-3.6M13 2.5v2.4h-2.4" />
            </svg>
          </button>
          {!fixed && onMinimize && (
            <button type="button" className="term-btn" onClick={onMinimize} title="Minimize" aria-label="Minimize">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                <path d="M3 12h10" />
              </svg>
            </button>
          )}
          {onSsh && (
            <button type="button" className="term-btn" onClick={onSsh} title="Connect via SSH" aria-label="Connect via SSH">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                <path d="M2 4h12v8H2zM4 6.5 6.5 8 4 9.5M8 10h4" />
              </svg>
            </button>
          )}
          {!fullPage && (
            <button type="button" className="term-btn" onClick={popOut} title="Open in a new window" aria-label="Pop out">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                <path d="M6 3H3v10h10v-3M9.5 2H14v4.5M14 2 7.5 8.5" />
              </svg>
            </button>
          )}
          <button type="button" className="term-btn" onClick={toggleFullscreen} title="Fullscreen" aria-label="Fullscreen">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M2 6V2h4M14 6V2h-4M2 10v4h4M14 10v4h-4" />
            </svg>
          </button>
          {onClose && (
            <button type="button" className="term-btn" onClick={onClose} title="Close" aria-label="Close">
              <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
                <path d="M4 4l8 8M12 4l-8 8" />
              </svg>
            </button>
          )}
        </span>
      </div>
      {/* The host stays mounted (and the session alive) while the window is hidden. */}
      <div className="term-host" ref={hostRef} />
      {reconnect.active && (
        <div className="term-reconnect" role="status">
          <span>
            Reconnecting{reconnect.seconds > 0 ? ` in ${reconnect.seconds}s` : ""}
            <span className="ellipsis" aria-hidden="true">
              <span>.</span>
              <span>.</span>
              <span>.</span>
            </span>
          </span>
          <button type="button" className="btn btn-small" onClick={() => reconnectNowRef.current()}>
            Reconnect now
          </button>
        </div>
      )}
    </div>
  );

  if (embedded) {
    return <div className="term-embed">{frame}</div>;
  }
  if (fullPage) {
    return <div className="term-page">{frame}</div>;
  }
  // No backdrop: the floating panel deliberately leaves the page (and its live
  // preview) visible and interactive behind it.
  return frame;
};

export default AppTerminal;
