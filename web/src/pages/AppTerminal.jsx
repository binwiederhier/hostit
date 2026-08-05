import { useEffect, useRef } from "react";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

// A shell in the app's container, in the browser. The websocket streams raw
// terminal bytes both ways (binary), and a text frame carries the size whenever
// the window changes -- the server maps that onto the pty. It is the same shell
// an SSH session gets, so it is owner-only on the server side.
const AppTerminal = ({ name, onClose }) => {
  const hostRef = useRef(null);

  useEffect(() => {
    const term = new Terminal({
      fontSize: 13,
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace",
      cursorBlink: true,
      theme: { background: "#14181d", foreground: "#e8ecf1", cursor: "#10b981" },
    });
    const fit = new FitAddon();
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
    ws.onopen = () => {
      sendSize();
      term.focus();
    };
    ws.onmessage = (e) => term.write(new Uint8Array(e.data));
    ws.onclose = () => term.write("\r\n\x1b[90m[session closed]\x1b[0m\r\n");
    ws.onerror = () => term.write("\r\n\x1b[31m[connection error]\x1b[0m\r\n");

    const dataSub = term.onData((d) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(encoder.encode(d));
      }
    });
    const onResize = () => sendSize();
    window.addEventListener("resize", onResize);

    return () => {
      window.removeEventListener("resize", onResize);
      dataSub.dispose();
      ws.close();
      term.dispose();
    };
  }, [name]);

  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true">
      <div className="term-window">
        <div className="term-bar">
          <span className="mono">{name} &mdash; terminal</span>
          <button type="button" className="btn btn-small" onClick={onClose}>
            Close
          </button>
        </div>
        <div className="term-host" ref={hostRef} />
      </div>
    </div>
  );
};

export default AppTerminal;
