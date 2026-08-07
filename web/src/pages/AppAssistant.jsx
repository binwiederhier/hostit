import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { marked } from "marked";
import DOMPurify from "dompurify";

// The in-browser coding agent. It POSTs a message to the daemon's assistant
// endpoint and reads the loop back as Server-Sent Events -- the model's thinking,
// its text, and every tool it runs. The conversation is persisted server-side, so
// a reload (or another device) picks it up where it left off.

marked.setOptions({ breaks: true, gfm: true });

// Fun status words that rotate while the assistant works, in the spirit of a
// certain CLI. Purely cosmetic.
const WORKING_WORDS = [
  "Working",
  "Baking",
  "Brewing",
  "Cooking",
  "Conjuring",
  "Crafting",
  "Noodling",
  "Percolating",
  "Pondering",
  "Simmering",
  "Tinkering",
  "Whirring",
];

// Bespoke, minimal outline icons per tool -- one shared 16x16 stroke style, drawn
// in the accent colour (see .asst-tool-svg). Replaces the emoji.
const TOOL_ICON_PATHS = {
  list_files: <path d="M2.2 4.4c0-.5.4-.9.9-.9h2.4l1.3 1.4h6.1c.5 0 .9.4.9.9v5.9c0 .5-.4.9-.9.9H3.1a.9.9 0 0 1-.9-.9z" />,
  read_file: (
    <>
      <path d="M9 2.6H5.1a.8.8 0 0 0-.8.8v9.2c0 .5.3.8.8.8h5.8a.8.8 0 0 0 .8-.8V5.5z" />
      <path d="M9 2.6v3h3M6.3 8.6h3.4M6.3 10.6h3.4" />
    </>
  ),
  write_file: (
    <>
      <path d="M10.6 3.1l2.3 2.3" />
      <path d="M11.7 1.9l2.4 2.4-8 8-3.2.8.8-3.2z" />
    </>
  ),
  run_command: (
    <>
      <rect x="2" y="3" width="12" height="10" rx="1.4" />
      <path d="M4.6 6.6 6.4 8l-1.8 1.4M8.4 10h3" />
    </>
  ),
  read_logs: <path d="M3.2 4.7h9.6M3.2 8h9.6M3.2 11.3h5.5" />,
  deploy: (
    <>
      <path d="M3 12.6h10" />
      <path d="M8 10V3.2M5.3 5.9 8 3.2l2.7 2.7" />
    </>
  ),
  refresh_preview: (
    <>
      <path d="M12.8 8a4.8 4.8 0 1 1-1.4-3.4" />
      <path d="M12.9 3.4V6H10.3" />
    </>
  ),
  snapshot: (
    <>
      <circle cx="8" cy="8" r="5.3" />
      <path d="M8 5.2V8l2 1.3" />
    </>
  ),
  list_snapshots: <path d="M6 4.6h6.5M6 8h6.5M6 11.4h6.5M3.4 4.6h.01M3.4 8h.01M3.4 11.4h.01" />,
  rollback: (
    <>
      <path d="M3.4 8a4.6 4.6 0 1 0 1.4-3.3" />
      <path d="M3.3 3.4V6H5.9" />
    </>
  ),
  _default: <path d="M8 2.6l4.7 2.7v5.4L8 13.4 3.3 10.7V5.3z" />,
};

const ToolIcon = ({ tool }) => (
  <svg className="asst-tool-svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    {TOOL_ICON_PATHS[tool] || TOOL_ICON_PATHS._default}
  </svg>
);

// A run of tool calls gets a "layers" mark.
const GroupIcon = () => (
  <svg className="asst-tool-svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M8 2.4l5.6 3-5.6 3-5.6-3z" />
    <path d="M2.6 8.6l5.4 2.9 5.4-2.9" />
  </svg>
);

const truncate = (s, n) => (s && s.length > n ? s.slice(0, n) + "…" : s || "");

// summarize turns a tool call into a short human line for the collapsed chip.
const summarize = (tool, input) => {
  let a = {};
  try {
    a = JSON.parse(input || "{}");
  } catch {
    // keep defaults
  }
  switch (tool) {
    case "list_files":
      return a.path ? `Listed ${a.path}/` : "Listed files";
    case "read_file":
      return `Read ${a.path}`;
    case "write_file":
      return `Wrote ${a.path}`;
    case "run_command":
      return `Ran ${truncate(a.command, 64)}`;
    case "read_logs":
      return "Read logs";
    case "deploy":
      return "Deployed the app";
    case "refresh_preview":
      return "Refreshed the preview";
    case "snapshot":
      return a.label ? `Saved snapshot "${truncate(a.label, 40)}"` : "Saved a snapshot";
    case "list_snapshots":
      return "Listed snapshots";
    case "rollback":
      return "Proposed a rollback";
    default:
      return tool;
  }
};

const prettyInput = (input) => {
  try {
    return JSON.stringify(JSON.parse(input), null, 2);
  } catch {
    return input || "";
  }
};

const Markdown = ({ text }) => {
  const html = useMemo(() => DOMPurify.sanitize(marked.parse(text || "")), [text]);
  return <div className="asst-md" dangerouslySetInnerHTML={{ __html: html }} />;
};

// A tool call, collapsed to a one-line summary by default; click to see the exact
// input and output. Shows a spinner while it runs and a red edge when it errored.
const ToolCall = ({ item }) => {
  const [open, setOpen] = useState(false);
  const running = item.output == null;
  const cls = ["asst-tool", item.isError ? "asst-tool-error" : "", running ? "asst-tool-running" : ""].join(" ").trim();
  return (
    <div className={cls}>
      <button type="button" className="asst-tool-head" onClick={() => setOpen((o) => !o)}>
        <span className="asst-tool-icon" aria-hidden="true">
          <ToolIcon tool={item.tool} />
        </span>
        <span className="asst-tool-summary">{summarize(item.tool, item.input)}</span>
        {running && <span className="asst-tool-spinner" aria-hidden="true" />}
        {!running && item.isError && <span className="asst-tool-badge">error</span>}
        <span className="asst-tool-chev" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
      </button>
      {open && (
        <div className="asst-tool-body">
          <div className="asst-tool-label">input</div>
          <pre className="asst-tool-pre">{prettyInput(item.input)}</pre>
          {item.output != null && (
            <>
              <div className="asst-tool-label">output</div>
              <pre className="asst-tool-pre">{item.output}</pre>
            </>
          )}
        </div>
      )}
    </div>
  );
};

// Consecutive tool calls collapse into one group -- expanded while they run so
// progress is visible, collapsed once done to keep the history tidy. Each call
// inside stays individually expandable.
const ToolGroup = ({ tools }) => {
  const running = tools.some((t) => t.output == null);
  const anyError = tools.some((t) => t.isError);
  const [override, setOverride] = useState(null); // null = follow the running state
  const open = override ?? running;
  const current = tools.find((t) => t.output == null) || tools[tools.length - 1];
  return (
    <div className="asst-group">
      <button type="button" className="asst-group-head" onClick={() => setOverride(!open)}>
        <span className="asst-tool-icon" aria-hidden="true">
          <GroupIcon />
        </span>
        <span className="asst-tool-summary">
          {running ? summarize(current.tool, current.input) : `${tools.length} actions`}
        </span>
        {running && <span className="asst-tool-spinner" aria-hidden="true" />}
        {!running && anyError && <span className="asst-tool-badge">error</span>}
        <span className="asst-tool-chev" aria-hidden="true">
          {open ? "▾" : "▸"}
        </span>
      </button>
      {open && (
        <div className="asst-group-body">
          {tools.map((t) => (
            <ToolCall key={t.id} item={t} />
          ))}
        </div>
      )}
    </div>
  );
};

// renderTranscript walks the items, folding runs of tool calls into groups.
const renderTranscript = (items) => {
  const out = [];
  let i = 0;
  while (i < items.length) {
    if (items[i].kind === "tool") {
      const group = [];
      while (i < items.length && items[i].kind === "tool") {
        group.push(items[i]);
        i++;
      }
      if (group.length === 1) {
        out.push(<ToolCall key={group[0].id} item={group[0]} />);
      } else {
        out.push(<ToolGroup key={group[0].id} tools={group} />);
      }
    } else {
      out.push(<Turn key={items[i].id} item={items[i]} />);
      i++;
    }
  }
  return out;
};

const Turn = ({ item }) => {
  switch (item.kind) {
    case "user":
      return <div className="asst-turn asst-user">{item.text}</div>;
    case "thinking":
      return <div className="asst-turn asst-thinking">{item.text}</div>;
    case "text":
      return (
        <div className="asst-turn asst-text">
          <Markdown text={item.text} />
        </div>
      );
    case "tool":
      return <ToolCall item={item} />;
    case "error":
      return <div className="asst-turn asst-error">{item.text}</div>;
    default:
      return null;
  }
};

// A rotating "Working... / Baking..." status shown while the assistant runs.
const WorkingIndicator = () => {
  const [i, setI] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setI((n) => (n + 1) % WORKING_WORDS.length), 2500);
    return () => clearInterval(t);
  }, []);
  return (
    <div className="asst-working">
      <span className="asst-working-spinner" aria-hidden="true" />
      <span>{WORKING_WORDS[i]}</span>
      <span className="ellipsis" aria-hidden="true">
        <span>.</span>
        <span>.</span>
        <span>.</span>
      </span>
    </div>
  );
};

const AppAssistant = ({ name, onClose, embedded = false, onPreviewRefresh }) => {
  const [items, setItems] = useState([]);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [confirm, setConfirm] = useState(null); // a pending action needing the owner's OK (rollback)
  const scrollRef = useRef(null);
  const taRef = useRef(null);

  // Grow the input with its content, up to a few lines, then scroll. Only show a
  // scrollbar once it actually overflows the cap, not at rest.
  useEffect(() => {
    const ta = taRef.current;
    if (ta) {
      ta.style.height = "auto";
      ta.style.height = `${Math.min(ta.scrollHeight, 140)}px`;
      ta.style.overflowY = ta.scrollHeight > 140 ? "auto" : "hidden";
    }
  }, [input]);

  // Load the persisted conversation and whether a turn is running -- what a reload
  // or another device continues from.
  const loadTranscript = useCallback(async () => {
    try {
      const r = await fetch(`/api/apps/${encodeURIComponent(name)}/assistant`, { credentials: "same-origin" });
      const data = r.ok ? await r.json() : { items: [], running: false };
      // Stable, position-based ids: the transcript is append-only and never
      // reorders, so keying by index means the done-reconcile reuses the existing
      // DOM instead of remounting the whole list (which flickered).
      setItems((data.items || []).map((it, idx) => ({ ...it, id: idx })));
      setBusy(!!data.running);
    } finally {
      setLoaded(true);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [name]);

  useEffect(() => {
    loadTranscript();
  }, [loadTranscript]);

  useEffect(() => {
    scrollRef.current?.scrollTo(0, scrollRef.current.scrollHeight);
  }, [items, busy]);

  // Apply one streamed event. The run is server-owned and broadcast, so these
  // arrive for anyone's turn on this app, not just ours.
  const handleEvent = useCallback((ev) => {
    if (ev.type === "done") {
      setBusy(false);
      loadTranscript(); // reconcile every watcher to the committed transcript
      return;
    }
    if (ev.type === "error") {
      setBusy(false);
      setItems((prev) => [...prev, { id: prev.length, kind: "error", text: ev.error }]);
      return;
    }
    if (ev.type === "confirm") {
      // A destructive action (rollback) the assistant proposed; the owner acts on it.
      // Kept out of the transcript so it survives the done-reconcile.
      let parsed = {};
      try {
        parsed = JSON.parse(ev.input || "{}");
      } catch {
        // no id
      }
      setConfirm({ tool: ev.tool, id: parsed.id || "" });
      return;
    }
    setBusy(true);
    setItems((prev) => {
      const next = [...prev];
      if (ev.type === "tool_result") {
        for (let i = next.length - 1; i >= 0; i--) {
          if (next[i].kind === "tool" && next[i].output == null) {
            next[i] = { ...next[i], output: ev.output ?? "", isError: ev.is_error };
            return next;
          }
        }
        return next;
      }
      if (ev.type === "user") next.push({ id: next.length, kind: "user", text: ev.text });
      else if (ev.type === "thinking" && (ev.text || "").trim()) next.push({ id: next.length, kind: "thinking", text: ev.text });
      else if (ev.type === "text") next.push({ id: next.length, kind: "text", text: ev.text });
      else if (ev.type === "tool_use") {
        next.push({ id: next.length, kind: "tool", tool: ev.tool, input: ev.input, output: null });
        // A deploy or an explicit refresh means the live preview is now stale; ask
        // the page hosting us to reload it. (A deploy also bumps app_started_at, so
        // this is belt-and-suspenders; a static-file refresh has only this signal.)
        if ((ev.tool === "deploy" || ev.tool === "refresh_preview") && onPreviewRefresh) {
          onPreviewRefresh();
        }
      }
      return next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loadTranscript, onPreviewRefresh]);

  // Live event stream (SSE). Every watcher subscribes, so a run started on any
  // device shows up here; EventSource reconnects on its own if the link drops.
  useEffect(() => {
    const es = new EventSource(`/api/apps/${encodeURIComponent(name)}/assistant/stream`);
    es.onmessage = (e) => {
      try {
        handleEvent(JSON.parse(e.data));
      } catch {
        // ignore a malformed frame
      }
    };
    return () => es.close();
  }, [name, handleEvent]);

  // Send a message: the server starts the turn in the background and everything
  // comes back on the stream. We do not render it optimistically -- the stream
  // echoes the message so every device shows it the same way.
  // Carry out a confirmed rollback (the only confirm-gated action so far).
  const runConfirm = async () => {
    const c = confirm;
    setConfirm(null);
    if (!c || c.tool !== "rollback" || !c.id) return;
    try {
      await fetch(`/api/apps/${encodeURIComponent(name)}/snapshots/${encodeURIComponent(c.id)}/restore`, {
        method: "POST",
        credentials: "same-origin",
      });
    } catch {
      // The app's state (and the preview) will reflect whether it took.
    }
  };

  const send = async () => {
    const message = input.trim();
    if (!message || busy) return;
    setConfirm(null); // a new message supersedes a pending confirmation
    setInput("");
    setBusy(true);
    try {
      const r = await fetch(`/api/apps/${encodeURIComponent(name)}/assistant`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        credentials: "same-origin",
        body: JSON.stringify({ message }),
      });
      if (r.status === 409) {
        return; // a turn is already running; it will stream in
      }
      if (!r.ok) {
        // The server sends {"error": "..."} (e.g. rate limited); show that, not raw JSON.
        const body = await r.json().catch(() => null);
        handleEvent({ type: "error", error: body?.error || `request failed (${r.status})` });
      }
    } catch (err) {
      handleEvent({ type: "error", error: err.message });
    }
  };

  // Stop the running turn. The server cancels it and publishes a done on the
  // stream, which flips us out of the busy state -- so we do not touch state here.
  const stop = async () => {
    try {
      await fetch(`/api/apps/${encodeURIComponent(name)}/assistant/stop`, {
        method: "POST",
        credentials: "same-origin",
      });
    } catch {
      // If the request fails the run continues; the stream stays the source of truth.
    }
  };

  // Escape closes the modal, matching the terminal.
  useEffect(() => {
    if (!onClose) return undefined;
    const onKey = (e) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  const onKeyDown = (e) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  };

  const inner = (
    <>
      {!embedded && (
        <header className="asst-header">
          <span className="asst-title">
            <span className="asst-title-app">{name}</span> &middot; AI assistant (preview)
          </span>
          <button type="button" className="term-btn asst-close" onClick={onClose} title="Close" aria-label="Close">
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
              <path d="M4 4l8 8M12 4l-8 8" />
            </svg>
          </button>
        </header>
      )}

      <div className="asst-transcript" ref={scrollRef}>
        {loaded && items.length === 0 && (
          <p className="asst-empty">
            Ask me to build or change <strong>{name}</strong> &mdash; in plain English. I can read and write its files
            and run commands in its container, then publish. Try: &ldquo;add a leaderboard&rdquo;.
          </p>
        )}
        {renderTranscript(items)}
        {busy && <WorkingIndicator />}
      </div>

      {confirm && confirm.tool === "rollback" && (
        <div className="asst-confirm" role="alertdialog">
          <div className="asst-confirm-text">
            Roll back <strong>{name}</strong> to snapshot <span className="mono">{confirm.id}</span>? This restores its
            files to that point (a safety snapshot of the current state is taken first).
          </div>
          <div className="asst-confirm-actions">
            <button type="button" className="btn btn-small" onClick={() => setConfirm(null)}>
              Cancel
            </button>
            <button type="button" className="btn btn-small btn-danger" onClick={runConfirm}>
              Roll back
            </button>
          </div>
        </div>
      )}

      <div className="asst-input">
        <textarea
          ref={taRef}
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder="Build or change your app..."
          rows={1}
          disabled={busy}
        />
        {busy ? (
          <button type="button" className="btn asst-send asst-stop" onClick={stop} title="Stop" aria-label="Stop">
            <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden="true">
              <rect x="4" y="4" width="8" height="8" rx="1.5" />
            </svg>
          </button>
        ) : (
          <button
            type="button"
            className="btn btn-primary asst-send"
            onClick={() => send()}
            disabled={!input.trim()}
            title="Send"
            aria-label="Send"
          >
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M3 8h10M9 4l4 4-4 4" />
            </svg>
          </button>
        )}
      </div>
    </>
  );

  // Embedded (Direction C): the chat is a panel on the app page, not a modal.
  if (embedded) {
    return <div className="asst-window asst-embedded">{inner}</div>;
  }
  return (
    <div className="modal-backdrop" role="dialog" aria-modal="true">
      <div className="asst-window">{inner}</div>
    </div>
  );
};

export default AppAssistant;
