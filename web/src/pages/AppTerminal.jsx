import { useEffect, useRef, useState } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

// A shell in the app's container, in the browser. The websocket streams raw
// terminal bytes both ways (binary), and a text frame carries the size whenever
// the window changes -- the server maps that onto the pty. It is the same login
// shell an SSH session gets (banner, colours, shortcuts), so it is owner-only on
// the server side.
//
// Two shapes: a floating panel on the app page (draggable, minimizable, and
// deliberately not dimming the page behind it so the live preview stays visible),
// and a full-window page reached by the pop-out (and directly at
// /app/<name>/terminal).
const AppTerminal = ({ name, onClose, onMinimize, onReady, onSessionEnd, onSsh, minimized = false, fullPage = false, embedded = false, active = true }) => {
  // embedded fills its container inline (a workspace view); fullPage is the fixed
  // full-window overlay. Both drop the floating panel's drag/minimize chrome.
  const fixed = fullPage || embedded;
  const hostRef = useRef(null);
  const frameRef = useRef(null);
  const fitRef = useRef(null);
  const termRef = useRef(null);
  const sendSizeRef = useRef(() => {});
  // Set just before we tear the socket down ourselves, so onclose can tell an
  // intentional close (unmount) from the server ending the session (e.g. a reboot).
  const closingRef = useRef(false);
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
    const ws = new WebSocket(`${proto}//${window.location.host}/api/apps/${encodeURIComponent(name)}/terminal`);
    ws.binaryType = "arraybuffer";
    const encoder = new TextEncoder();

    const sendSize = () => {
      fit.fit();
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ cols: term.cols, rows: term.rows }));
      }
    };
    sendSizeRef.current = sendSize;
    ws.onopen = () => {
      sendSize();
      term.focus();
      if (onReady) {
        onReady(); // the shell is connected; the page can stop showing "connecting"
      }
    };
    ws.onmessage = (e) => term.write(new Uint8Array(e.data));
    ws.onclose = () => {
      term.write("\r\n\x1b[90m[session closed]\x1b[0m\r\n");
      // The server ended the session (a reboot/poweroff killed the container, or
      // the time cap hit) rather than us tearing it down: let the page reflect it.
      if (!closingRef.current && onSessionEnd) {
        onSessionEnd();
      }
    };
    ws.onerror = () => term.write("\r\n\x1b[31m[connection error]\x1b[0m\r\n");

    const dataSub = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) {
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
      window.removeEventListener("resize", onResize);
      document.removeEventListener("fullscreenchange", onResize);
      ro.disconnect();
      dataSub.dispose();
      closingRef.current = true; // our own teardown, not a server-side session end
      ws.close();
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
  // cursor in it -- you can start typing without clicking first.
  useEffect(() => {
    if (!active) return;
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
    window.open(`/app/${encodeURIComponent(name)}/terminal`, "_blank", "width=960,height=600");
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
